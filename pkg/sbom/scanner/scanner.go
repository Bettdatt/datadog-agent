// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package scanner holds scanner related files
package scanner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"

	"github.com/DataDog/datadog-agent/comp/core/config"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	"github.com/DataDog/datadog-agent/pkg/sbom"
	"github.com/DataDog/datadog-agent/pkg/sbom/collectors"
	"github.com/DataDog/datadog-agent/pkg/sbom/telemetry"
	"github.com/DataDog/datadog-agent/pkg/util/filesystem"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

const (
	defaultScanTimeout = 30 * time.Second
	sendTimeout        = 5 * time.Second
)

var (
	globalScanner *Scanner
)

type scannerConfig struct {
	cacheCleanInterval time.Duration
}

// Scanner defines the scanner
type Scanner struct {
	cfg scannerConfig

	startOnce sync.Once
	running   bool
	disk      filesystem.Disk
	// scanQueue is the workqueue used to process scan requests
	scanQueue workqueue.TypedRateLimitingInterface[sbom.ScanRequest]
	// cacheMutex is used to protect the cache from concurrent access
	// It cannot be cleaned when a scan is running
	cacheMutex sync.Mutex

	wmeta      option.Option[workloadmeta.Component]
	collectors map[string]collectors.Collector
}

// NewScanner creates a new SBOM scanner. Call Start to start the store and its
// collectors.
func NewScanner(cfg config.Component, collectors map[string]collectors.Collector, wmeta option.Option[workloadmeta.Component]) *Scanner {
	return &Scanner{
		scanQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[sbom.ScanRequest](
				cfg.GetDuration("sbom.scan_queue.base_backoff"),
				cfg.GetDuration("sbom.scan_queue.max_backoff"),
			),
			workqueue.TypedRateLimitingQueueConfig[sbom.ScanRequest]{
				Name:            telemetry.Subsystem,
				MetricsProvider: telemetry.QueueMetricsProvider,
			},
		),
		disk:  filesystem.NewDisk(),
		wmeta: wmeta,
		cfg: scannerConfig{
			cfg.GetDuration("sbom.cache.clean_interval"),
		},
		collectors: collectors,
	}
}

// CreateGlobalScanner creates a SBOM scanner, sets it as the default
// global one, and returns it. Start() needs to be called before any data
// collection happens.
func CreateGlobalScanner(cfg config.Component, wmeta option.Option[workloadmeta.Component]) (*Scanner, error) {
	if globalScanner != nil {
		return nil, errors.New("global SBOM scanner already set, should only happen once")
	}

	for name, collector := range collectors.Collectors {
		if err := collector.Init(cfg, wmeta); err != nil {
			return nil, fmt.Errorf("failed to initialize SBOM collector '%s': %w", name, err)
		}
	}

	globalScanner = NewScanner(cfg, collectors.Collectors, wmeta)
	return globalScanner, nil
}

// SetGlobalScanner sets a global instance of the SBOM scanner. It should be
// used only for testing purposes.
func SetGlobalScanner(s *Scanner) {
	globalScanner = s
}

// GetGlobalScanner returns a global instance of the SBOM scanner. It does
// not create one if it's not already set (see CreateGlobalScanner) and returns
// nil in that case.
func GetGlobalScanner() *Scanner {
	return globalScanner
}

// Start starts the scanner
func (s *Scanner) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.start(ctx)
	})
}

// Scan enqueues a scan request to the scanner
func (s *Scanner) Scan(request sbom.ScanRequest) error {
	if s.scanQueue == nil {
		return errors.New("scanner not started")
	}
	s.scanQueue.Add(request)
	return nil
}

func (s *Scanner) enoughDiskSpace(opts sbom.ScanOptions, imgMeta *workloadmeta.ContainerImageMetadata) error {
	if !opts.CheckDiskUsage {
		return nil
	}

	usage, err := s.disk.GetUsage("/")
	if err != nil {
		return err
	}

	if usage.Available < opts.MinAvailableDisk {
		return fmt.Errorf("not enough disk space to safely collect sbom, %d available, %d required", usage.Available, opts.MinAvailableDisk)
	}

	if imgMeta == nil || opts.OverlayFsScan || opts.UseMount {
		return nil
	}

	// Check that we have either the minimum amount of disk space required for a scan
	// or 20% more than the size of the image
	sizeRequired := uint64(float64(imgMeta.SizeBytes) * 1.2)
	if usage.Available < sizeRequired {
		return fmt.Errorf("not enough disk space to safely collect sbom, %d available, %d required", usage.Available, sizeRequired)
	}

	return nil
}

// sendResult sends a ScanResult to the channel associated with the collector.
// It adds an error in the scan result if the operation fails.
func sendResult(ctx context.Context, requestID string, result *sbom.ScanResult, collector collectors.Collector) {
	if result == nil {
		log.Errorf("nil result for '%s'", requestID)
		return
	}
	channel := collector.Channel()
	if channel == nil {
		result.Error = fmt.Errorf("nil channel for '%s'", requestID)
		log.Errorf("%s", result.Error)
		return
	}
	select {
	case channel <- *result:
	case <-ctx.Done():
		result.Error = fmt.Errorf("context cancelled while sending scan result for '%s'", requestID)
	case <-time.After(sendTimeout):
		result.Error = fmt.Errorf("timeout while sending scan result for '%s'", requestID)
		log.Errorf("%s", result.Error)
	}
}

// startCacheCleaner periodically cleans the SBOM cache of all collectors
func (s *Scanner) startCacheCleaner(ctx context.Context) {
	cleanTicker := time.NewTicker(s.cfg.cacheCleanInterval)
	defer func() {
		cleanTicker.Stop()
		s.running = false
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-cleanTicker.C:
				s.cacheMutex.Lock()
				log.Debug("cleaning SBOM cache")
				for _, collector := range s.collectors {
					if err := collector.CleanCache(); err != nil {
						log.Warnf("could not clean SBOM cache: %v", err)
					}
				}
				s.cacheMutex.Unlock()
			}
		}
	}()
}

func (s *Scanner) start(ctx context.Context) {
	if s.running {
		return
	}
	s.running = true
	s.startCacheCleaner(ctx)
	s.startScanRequestHandler(ctx)
}

func (s *Scanner) startScanRequestHandler(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.scanQueue.ShutDown()
	}()
	go func() {
		for {
			r, shutdown := s.scanQueue.Get()
			if shutdown {
				break
			}
			s.handleScanRequest(ctx, r)
			s.scanQueue.Done(r)
		}
		for _, collector := range s.collectors {
			collector.Shutdown()
		}
	}()
}

// GetCollector returns the collector with the specified name
func (s *Scanner) GetCollector(collector string) collectors.Collector {
	return s.collectors[collector]
}

func (s *Scanner) handleScanRequest(ctx context.Context, request sbom.ScanRequest) {
	collector := s.GetCollector(request.Collector())
	if collector == nil {
		log.Errorf("invalid collector '%s'", request.Collector())
		s.scanQueue.Forget(request)
		return
	}

	telemetry.SBOMAttempts.Inc(request.Collector(), request.Type(collector.Options()))

	var imgMeta *workloadmeta.ContainerImageMetadata
	if collector.Type() == collectors.ContainerImageScanType {
		imgMeta = s.getImageMetadata(request)
		if imgMeta == nil {
			return
		}
	}
	s.processScan(ctx, request, imgMeta, collector)
}

// getImageMetadata returns the image metadata if the collector is a container image collector
// and the metadata is found in the store.
func (s *Scanner) getImageMetadata(request sbom.ScanRequest) *workloadmeta.ContainerImageMetadata {
	store, ok := s.wmeta.Get()
	if !ok {
		log.Errorf("workloadmeta store is not initialized")
		s.scanQueue.AddRateLimited(request)
		return nil
	}
	img, err := store.GetImage(request.ID())
	if err != nil || img == nil {
		log.Debugf("image metadata not found for image id %s: %s", request.ID(), err)
		s.scanQueue.Forget(request)
		return nil
	}
	return img
}

func (s *Scanner) processScan(ctx context.Context, request sbom.ScanRequest, imgMeta *workloadmeta.ContainerImageMetadata, collector collectors.Collector) {
	result := s.checkDiskSpace(imgMeta, collector)
	errorType := "disk_space"

	if result == nil {
		scanContext, cancel := context.WithTimeout(ctx, timeout(collector))
		defer cancel()
		result = s.PerformScan(scanContext, request, collector)
		errorType = "scan"
	}
	sendResult(ctx, request.ID(), result, collector)
	s.handleScanResult(result, collector, request, errorType)
	waitAfterScanIfNecessary(ctx, collector)
}

// checkDiskSpace checks if there is enough disk space to perform the scan
// It sends a scan result wrapping an error if there is not enough space
// If everything is correct it returns nil.
func (s *Scanner) checkDiskSpace(imgMeta *workloadmeta.ContainerImageMetadata, collector collectors.Collector) *sbom.ScanResult {
	err := s.enoughDiskSpace(collector.Options(), imgMeta)
	if err == nil {
		return nil
	}
	result := &sbom.ScanResult{
		ImgMeta: imgMeta,
		Error:   fmt.Errorf("failed to check current disk usage: %w", err),
	}
	return result
}

// PerformScan processes a scan request with the selected collector and returns the SBOM
func (s *Scanner) PerformScan(ctx context.Context, request sbom.ScanRequest, collector collectors.Collector) *sbom.ScanResult {
	createdAt := time.Now()

	s.cacheMutex.Lock()
	scanResult := collector.Scan(ctx, request)
	s.cacheMutex.Unlock()

	generationDuration := time.Since(createdAt)

	scanResult.CreatedAt = createdAt
	scanResult.Duration = generationDuration
	return &scanResult
}

func (s *Scanner) handleScanResult(scanResult *sbom.ScanResult, collector collectors.Collector, request sbom.ScanRequest, errorType string) {
	if scanResult == nil {
		telemetry.SBOMFailures.Inc(request.Collector(), request.Type(collector.Options()), "nil_scan_result")
		log.Errorf("nil scan result for '%s'", request.ID())
		return
	}
	if scanResult.Error != nil {
		telemetry.SBOMFailures.Inc(request.Collector(), request.Type(collector.Options()), errorType)
		s.scanQueue.AddRateLimited(request)
		return
	}

	telemetry.SBOMGenerationDuration.Observe(scanResult.Duration.Seconds(), request.Collector(), request.Type(collector.Options()))
	s.scanQueue.Forget(request)
}

func waitAfterScanIfNecessary(ctx context.Context, collector collectors.Collector) {
	wait := collector.Options().WaitAfter
	if wait == 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func timeout(collector collectors.Collector) time.Duration {
	scanTimeout := collector.Options().Timeout
	if scanTimeout == 0 {
		scanTimeout = defaultScanTimeout
	}
	return scanTimeout
}
