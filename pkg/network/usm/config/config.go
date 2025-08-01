// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package config provides helpers for USM configuration
package config

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/DataDog/datadog-agent/pkg/ebpf/kernelbugs"
	"github.com/DataDog/datadog-agent/pkg/network/config"
	"github.com/DataDog/datadog-agent/pkg/util/kernel"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// MinimumKernelVersion indicates the minimum kernel version required for HTTP monitoring
var MinimumKernelVersion kernel.Version

// NetifReceiveSKBCoreKprobeMaximumKernelVersion indicates the maximum kernel version to use with the __netif_receive_skb_core kprobe
var NetifReceiveSKBCoreKprobeMaximumKernelVersion kernel.Version

// ErrNotSupported is the error returned if USM is not supported on this platform
var ErrNotSupported = errors.New("Universal Service Monitoring (USM) is not supported")

func init() {
	MinimumKernelVersion = kernel.VersionCode(4, 14, 0)
	NetifReceiveSKBCoreKprobeMaximumKernelVersion = kernel.VersionCode(4, 15, 0)
}

func runningOnARM() bool {
	return strings.HasPrefix(runtime.GOARCH, "arm")
}

// TLSSupported returns true if HTTPs monitoring is supported on the current OS.
// We only support ARM with kernel >= 5.5.0 and with runtime compilation enabled
func TLSSupported(c *config.Config) bool {
	kversion, err := kernel.HostVersion()
	if err != nil {
		log.Warn("could not determine the current kernel version. https monitoring disabled.")
		return false
	}

	if runningOnARM() {
		return kversion >= kernel.VersionCode(5, 5, 0) && (c.EnableRuntimeCompiler || c.EnableCORE)
	}

	return kversion >= MinimumKernelVersion
}

// UretprobeSupported returns true if uretprobes are supported on this system.
// This checks for the kernel bug that causes segfaults with uretprobes and seccomp filters.
func UretprobeSupported() bool {
	hasUretprobeBug, err := kernelbugs.HasUretprobeSyscallSeccompBug()
	if err != nil {
		log.Errorf("failed to check for uretprobe syscall seccomp bug: %v", err)
		return false
	}
	if hasUretprobeBug {
		log.Warn("uretprobe-based monitoring disabled due to kernel bug that causes segmentation faults with uretprobes and seccomp filters")
		return false
	}

	return true
}

var (
	mu                    sync.Mutex
	isKernelVersionCached bool
	cachedKernelVersion   kernel.Version
)

// GetCachedKernelVersion returns the cached kernel version
func GetCachedKernelVersion() kernel.Version {
	mu.Lock()
	defer mu.Unlock()
	return cachedKernelVersion
}

// SetCachedKernelVersion sets the cached kernel version
func SetCachedKernelVersion(version kernel.Version) {
	mu.Lock()
	defer mu.Unlock()
	isKernelVersionCached = true
	cachedKernelVersion = version
}

// CheckUSMSupported returns an error if USM is not supported
// on this platform. Callers can check `errors.Is(err, ErrNotSupported)`
// to verify if USM is supported
func CheckUSMSupported(cfg *config.Config) error {
	// TODO: remove this once USM is supported on ebpf-less
	if cfg.EnableEbpfless {
		return fmt.Errorf("%w: eBPF-less is not supported", ErrNotSupported)
	}

	kversion, err := kernel.HostVersion()
	if err != nil {
		return fmt.Errorf("%w: could not determine the current kernel version: %w", ErrNotSupported, err)
	}

	if kversion < MinimumKernelVersion {
		return fmt.Errorf("%w: a Linux kernel version of %s or higher is required; we detected %s", ErrNotSupported, MinimumKernelVersion, kversion)
	}

	SetCachedKernelVersion(kversion)
	return nil
}

// IsUSMSupportedAndEnabled returns true if USM is supported and enabled
func IsUSMSupportedAndEnabled(config *config.Config) bool {
	// http.Supported is misleading, it should be named usm.Supported.
	return config.ServiceMonitoringEnabled && CheckUSMSupported(config) == nil
}

// NeedProcessMonitor returns true if the process monitor is needed for the given configuration
func NeedProcessMonitor(config *config.Config) bool {
	return config.EnableNativeTLSMonitoring || config.EnableGoTLSSupport || config.EnableIstioMonitoring || config.EnableNodeJSMonitoring
}

// ShouldUseNetifReceiveSKBCoreKprobe returns true if the __netif_receive_skb_core kprobe should be used.
func ShouldUseNetifReceiveSKBCoreKprobe() bool {
	mu.Lock()
	isCached := isKernelVersionCached
	mu.Unlock()
	if !isCached {
		kversion, err := kernel.HostVersion()
		if err != nil {
			log.Warnf("could not determine the current kernel version: %s", err)
			return false
		}
		SetCachedKernelVersion(kversion)
	}
	return GetCachedKernelVersion() < NetifReceiveSKBCoreKprobeMaximumKernelVersion
}
