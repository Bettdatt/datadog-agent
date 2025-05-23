// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

//go:generate $GOPATH/bin/include_headers pkg/collector/corechecks/ebpf/c/runtime/oom-kill-kern.c pkg/ebpf/bytecode/build/runtime/oom-kill.c pkg/ebpf/c
//go:generate $GOPATH/bin/integrity pkg/ebpf/bytecode/build/runtime/oom-kill.c pkg/ebpf/bytecode/runtime/oom-kill.go runtime

// Package oomkill is the system-probe side of the OOM Kill check
package oomkill

import (
	"fmt"

	"golang.org/x/sys/unix"

	manager "github.com/DataDog/ebpf-manager"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/ebpf/probe/oomkill/model"
	"github.com/DataDog/datadog-agent/pkg/ebpf"
	"github.com/DataDog/datadog-agent/pkg/ebpf/bytecode"
	"github.com/DataDog/datadog-agent/pkg/ebpf/bytecode/runtime"
	"github.com/DataDog/datadog-agent/pkg/ebpf/maps"
	"github.com/DataDog/datadog-agent/pkg/util/kernel"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const oomMapName = "oom_stats"

// Probe is the eBPF side of the OOM Kill check
type Probe struct {
	m      *manager.Manager
	oomMap *maps.GenericMap[uint64, oomStats]
}

// NewProbe creates a [Probe]
func NewProbe(cfg *ebpf.Config) (*Probe, error) {
	if cfg.EnableCORE {
		probe, err := loadOOMKillCOREProbe()
		if err == nil {
			return probe, nil
		}

		if !cfg.AllowRuntimeCompiledFallback {
			return nil, fmt.Errorf("error loading CO-RE oom-kill probe: %s. set system_probe_config.allow_runtime_compiled_fallback to true to allow fallback to runtime compilation", err)
		}
		log.Warnf("error loading CO-RE oom-kill probe: %s. falling back to runtime compiled probe", err)
	}

	return loadOOMKillRuntimeCompiledProbe(cfg)
}

func loadOOMKillCOREProbe() (*Probe, error) {
	kv, err := kernel.HostVersion()
	if err != nil {
		return nil, fmt.Errorf("error detecting kernel version: %s", err)
	}
	if kv < kernel.VersionCode(4, 9, 0) {
		return nil, fmt.Errorf("detected kernel version %s, but oom-kill probe requires a kernel version of at least 4.9.0", kv)
	}

	var probe *Probe
	err = ebpf.LoadCOREAsset("oom-kill.o", func(buf bytecode.AssetReader, opts manager.Options) error {
		probe, err = startOOMKillProbe(buf, opts)
		return err
	})
	if err != nil {
		return nil, err
	}

	log.Debugf("successfully loaded CO-RE version of oom-kill probe")
	return probe, nil
}

func loadOOMKillRuntimeCompiledProbe(cfg *ebpf.Config) (*Probe, error) {
	buf, err := runtime.OomKill.Compile(cfg, getCFlags(cfg))
	if err != nil {
		return nil, err
	}
	defer buf.Close()

	return startOOMKillProbe(buf, manager.Options{})
}

func getCFlags(config *ebpf.Config) []string {
	cflags := []string{"-g"}
	if config.BPFDebug {
		cflags = append(cflags, "-DDEBUG=1")
	}
	return cflags
}

func startOOMKillProbe(buf bytecode.AssetReader, managerOptions manager.Options) (*Probe, error) {
	m := &manager.Manager{
		Probes: []*manager.Probe{
			{ProbeIdentificationPair: manager.ProbeIdentificationPair{EBPFFuncName: "kprobe__oom_kill_process", UID: "oom"}},
		},
		Maps: []*manager.Map{
			{Name: "oom_stats"},
		},
	}

	managerOptions.RemoveRlimit = true

	if err := m.InitWithOptions(buf, managerOptions); err != nil {
		return nil, fmt.Errorf("failed to init manager: %w", err)
	}

	if err := m.Start(); err != nil {
		return nil, fmt.Errorf("failed to start manager: %w", err)
	}

	oomMap, err := maps.GetMap[uint64, oomStats](m, oomMapName)
	if err != nil {
		return nil, fmt.Errorf("failed to get map '%s': %w", oomMapName, err)
	}
	ebpf.AddNameMappings(m, "oom_kill")
	ebpf.AddProbeFDMappings(m)

	return &Probe{
		m:      m,
		oomMap: oomMap,
	}, nil
}

// Close releases all associated resources
func (k *Probe) Close() {
	ebpf.RemoveNameMappings(k.m)
	if err := k.m.Stop(manager.CleanAll); err != nil {
		log.Errorf("error stopping OOM Kill: %s", err)
	}
}

// GetAndFlush gets the stats
func (k *Probe) GetAndFlush() (results []model.OOMKillStats) {
	var allTimestamps []uint64
	var ts uint64
	var stat oomStats
	it := k.oomMap.Iterate()
	for it.Next(&ts, &stat) {
		results = append(results, convertStats(stat))
		allTimestamps = append(allTimestamps, ts)
	}

	if err := it.Err(); err != nil {
		log.Warnf("failed to iterate on OOM stats while flushing: %s", err)
	}

	for _, ts := range allTimestamps {
		if err := k.oomMap.Delete(&ts); err != nil {
			log.Warnf("failed to delete stat: %s", err)
		}
	}

	return results
}

func convertStats(in oomStats) (out model.OOMKillStats) {
	out.CgroupName = unix.ByteSliceToString(in.Cgroup_name[:])
	out.VictimPid = in.Victim_pid
	out.TriggerPid = in.Trigger_pid
	out.Score = in.Score
	out.ScoreAdj = in.Score_adj
	out.VictimComm = unix.ByteSliceToString(in.Victim_comm[:])
	out.TriggerComm = unix.ByteSliceToString(in.Trigger_comm[:])
	out.Pages = in.Pages
	out.MemCgOOM = in.Memcg_oom
	return
}
