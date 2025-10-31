// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2025-present Datadog, Inc.

//go:build linux_bpf

package procscan

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/btree"
	"golang.org/x/time/rate"

	"github.com/DataDog/datadog-agent/pkg/discovery/tracermetadata"
	"github.com/DataDog/datadog-agent/pkg/dyninst/process"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// ProcessID is a unique identifier for a process.
type ProcessID uint32

// Scanner discovers Go processes for instrumentation using a watermark-based
// algorithm. Processes are analyzed exactly once when they've been alive for
// at least startDelay. Processes that exit before startDelay are never
// analyzed.
//
// Thread-safety: not thread-safe, use from a single goroutine only.
type Scanner struct {

	// startDelay is how long a process must be alive before analysis.
	startDelay ticks

	// lastWatermark is the upper bound of the previous scan's time window.
	lastWatermark ticks

	// nowTicks returns the current time in ticks since boot.
	nowTicks func() (ticks, error)

	// live tracks discovered processes that have been reported as live.
	live *btree.BTreeG[uint32]

	// listPids returns an iterator over all PIDs in the system.
	listPids func() iter.Seq2[uint32, error]

	// readStartTime reads the start time of a process in ticks since boot.
	readStartTime func(pid int32) (ticks, error)

	// tracerMetadataReader reads tracer metadata from a process.
	tracerMetadataReader func(pid int32) (tracermetadata.TracerMetadata, error)

	// resolveExecutable resolves the executable metadata for a process.
	resolveExecutable func(pid int32) (process.Executable, error)
}

// NewScanner creates a new Scanner that discovers processes in the given
// procfs root that have been alive for at least startDelay.
func NewScanner(
	procfsRoot string,
	startDelay time.Duration,
) *Scanner {
	startDelayTicks := ticks(
		(startDelay.Nanoseconds() * int64(clkTck)) / time.Second.Nanoseconds(),
	)
	reader := newStartTimeReader(procfsRoot)
	return newScanner(
		startDelayTicks,
		nowTicks,
		func() iter.Seq2[uint32, error] {
			return listPids(procfsRoot, 512)
		},
		func(pid int32) (ticks, error) {
			startTime, err := reader.read(pid)
			if err != nil {
				return 0, err
			}
			return ticks(startTime), nil
		},
		func(pid int32) (tracermetadata.TracerMetadata, error) {
			return tracermetadata.GetTracerMetadata(int(pid), procfsRoot)
		},
		func(pid int32) (process.Executable, error) {
			return resolveExecutable(procfsRoot, pid)
		},
	)
}

// newScanner creates a Scanner with injected dependencies. Used by NewScanner
// for production code and by tests for dependency injection.
func newScanner(
	startDelay ticks,
	nowTicks func() (ticks, error),
	listPids func() iter.Seq2[uint32, error],
	readStartTime func(pid int32) (ticks, error),
	tracerMetadataReader func(pid int32) (tracermetadata.TracerMetadata, error),
	resolveExecutable func(pid int32) (process.Executable, error),
) *Scanner {
	return &Scanner{
		startDelay:           startDelay,
		nowTicks:             nowTicks,
		listPids:             listPids,
		readStartTime:        readStartTime,
		tracerMetadataReader: tracerMetadataReader,
		resolveExecutable:    resolveExecutable,
		live:                 btree.NewG(16, cmp.Less[uint32]),
	}
}

// DiscoveredProcess represents a newly discovered process that should be
// instrumented.
type DiscoveredProcess struct {
	PID            uint32
	StartTimeTicks uint64
	tracermetadata.TracerMetadata
	Executable process.Executable
}

// scannerLogLimiter rate-limits non-interesting errors during scanning to
// avoid log spam from common transient errors like ENOENT and ESRCH.
var scannerLogLimiter = rate.NewLimiter(rate.Every(10*time.Minute), 10)

// Scan discovers new Go processes and detects removed processes since the last
// Scan call.
//
// Returns:
//   - new: Processes discovered in this scan
//   - removed: Processes that have exited since the last scan
//   - err: Fatal error that prevented the scan from completing
func (p *Scanner) Scan() (
	new []DiscoveredProcess,
	removed []ProcessID,
	err error,
) {
	now, err := p.nowTicks()
	if err != nil {
		return nil, nil, fmt.Errorf("get timestamp: %w", err)
	}

	nextWatermark := now - p.startDelay

	// Handle edge cases: machine just booted or clock went backward.
	if now < p.startDelay || nextWatermark < p.lastWatermark {
		nextWatermark = now
	}

	// Rate-limit logging about errors that are interesting.
	maybeLogErr := func(prefix string, err error) {
		if err == nil ||
			// These errors are expected and not interesting (process may have
			// exited, etc).
			errors.Is(err, fs.ErrNotExist) ||
			errors.Is(err, fs.ErrPermission) ||
			errors.Is(err, syscall.ESRCH) {
			return
		}
		if scannerLogLimiter.Allow() {
			log.Warnf("scanner: %s: %v", prefix, err)
		} else {
			log.Tracef("scanner: %s: %v", prefix, err)
		}
	}

	// Clone the live set. Processes still alive will be removed from this
	// clone. Whatever remains has exited.
	noLongerLive := p.live.Clone()
	var ret []DiscoveredProcess

	for pid, err := range p.listPids() {
		if err != nil {
			return nil, nil, fmt.Errorf("list pids: %w", err)
		}

		// Skip processes we've already discovered.
		if _, ok := noLongerLive.Delete(pid); ok {
			continue
		}

		// Only analyze processes in the watermark window.
		startTime, err := p.readStartTime(int32(pid))
		if err != nil {
			maybeLogErr("read start time", err)
			continue
		}
		if startTime < p.lastWatermark || startTime > nextWatermark {
			continue
		}

		// Only instrument Go processes.
		tracerMetadata, err := p.tracerMetadataReader(int32(pid))
		if err != nil {
			continue
		}
		if tracerMetadata.TracerLanguage != "go" {
			continue
		}

		executable, err := p.resolveExecutable(int32(pid))
		if err != nil {
			maybeLogErr("resolve executable", err)
			continue
		}

		p.live.ReplaceOrInsert(pid)
		ret = append(ret, DiscoveredProcess{
			PID:            pid,
			StartTimeTicks: uint64(startTime),
			TracerMetadata: tracerMetadata,
			Executable:     executable,
		})
	}

	removed = make([]ProcessID, 0, noLongerLive.Len())
	noLongerLive.Ascend(func(pid uint32) bool {
		removed = append(removed, ProcessID(pid))
		return true
	})
	noLongerLive.Clear(true)

	p.lastWatermark = nextWatermark
	return ret, removed, nil
}

func resolveExecutable(procfsRoot string, pid int32) (process.Executable, error) {
	exeLink := filepath.Join(procfsRoot, strconv.Itoa(int(pid)), "exe")
	exePath, err := os.Readlink(exeLink)
	if err != nil {
		return process.Executable{}, err
	}

	openPath := exePath
	file, err := os.Open(openPath)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		trimmed := strings.TrimPrefix(openPath, "/")
		if trimmed != "" {
			openPath = filepath.Join(
				procfsRoot,
				strconv.Itoa(int(pid)),
				"root",
				trimmed,
			)
			file, err = os.Open(openPath)
		}
	}
	if err != nil {
		return process.Executable{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return process.Executable{}, err
	}
	statT, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return process.Executable{}, fmt.Errorf(
			"unexpected stat type %T", info.Sys(),
		)
	}
	key := process.FileKey{
		FileHandle: process.FileHandle{
			Dev: uint64(statT.Dev),
			Ino: statT.Ino,
		},
		LastModified: statT.Mtim,
	}
	return process.Executable{
		Path: openPath,
		Key:  key,
	}, nil
}
