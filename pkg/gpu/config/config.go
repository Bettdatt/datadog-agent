// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package config provides the GPU monitoring config.
package config

import (
	"errors"
	"time"

	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/ebpf"
	"github.com/DataDog/datadog-agent/pkg/gpu/config/consts"
	sysconfig "github.com/DataDog/datadog-agent/pkg/system-probe/config"
)

// ErrNotSupported is the error returned if GPU monitoring is not supported on this platform
var ErrNotSupported = errors.New("GPU Monitoring is not supported")

// Config holds the configuration for the GPU monitoring probe.
type Config struct {
	ebpf.Config
	// Enabled indicates whether the GPU monitoring probe is enabled.
	Enabled bool
	// ScanProcessesInterval is the interval at which the probe scans for new or terminated processes.
	ScanProcessesInterval time.Duration
	// InitialProcessSync indicates whether the probe should sync the process list on startup.
	InitialProcessSync bool
	// ConfigureCgroupPerms indicates whether the probe should configure cgroup permissions for GPU monitoring
	ConfigureCgroupPerms bool
	// EnableFatbinParsing indicates whether the probe should enable fatbin parsing.
	EnableFatbinParsing bool
	// KernelCacheQueueSize is the size of the kernel cache queue for parsing requests
	KernelCacheQueueSize int
	// RingBufferSizePagesPerDevice is the number of pages to use for the ring buffer per device.
	RingBufferSizePagesPerDevice int
	// MaxKernelLaunchesPerStream is the maximum number of kernel launches to process per stream before forcing a sync.
	MaxKernelLaunchesPerStream int
	// MaxMemAllocEventsPerStream is the maximum number of memory allocation events to process per stream before evicting the oldest events.
	MaxMemAllocEventsPerStream int
	// MaxPendingKernelSpans is the maximum number of pending kernel spans to keep in each stream handler.
	MaxPendingKernelSpans int
	// MaxPendingMemorySpans is the maximum number of pending memory allocation spans to keep in each stream handler.
	MaxPendingMemorySpans int
	// MaxStreams is the maximum number of streams that can be processed concurrently.
	MaxStreams int
	// MaxStreamInactivity is the maximum time to wait for a stream to be inactive before flushing it.
	MaxStreamInactivity time.Duration
}

// New generates a new configuration for the GPU monitoring probe.
func New() *Config {
	spCfg := pkgconfigsetup.SystemProbe()
	return &Config{
		Config:                       *ebpf.NewConfig(),
		ScanProcessesInterval:        time.Duration(spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "process_scan_interval_seconds"))) * time.Second,
		InitialProcessSync:           spCfg.GetBool(sysconfig.FullKeyPath(consts.GPUNS, "initial_process_sync")),
		Enabled:                      spCfg.GetBool(sysconfig.FullKeyPath(consts.GPUNS, "enabled")),
		ConfigureCgroupPerms:         spCfg.GetBool(sysconfig.FullKeyPath(consts.GPUNS, "configure_cgroup_perms")),
		EnableFatbinParsing:          spCfg.GetBool(sysconfig.FullKeyPath(consts.GPUNS, "enable_fatbin_parsing")),
		KernelCacheQueueSize:         spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "fatbin_request_queue_size")),
		RingBufferSizePagesPerDevice: spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "ring_buffer_pages_per_device")),
		MaxKernelLaunchesPerStream:   spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_kernel_launches_per_stream")),
		MaxMemAllocEventsPerStream:   spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_mem_alloc_events_per_stream")),
		MaxPendingKernelSpans:        spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_pending_kernel_spans_per_stream")),
		MaxPendingMemorySpans:        spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_pending_memory_spans_per_stream")),
		MaxStreams:                   spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_streams")),
		MaxStreamInactivity:          time.Duration(spCfg.GetInt(sysconfig.FullKeyPath(consts.GPUNS, "max_stream_inactivity_seconds"))) * time.Second,
	}
}
