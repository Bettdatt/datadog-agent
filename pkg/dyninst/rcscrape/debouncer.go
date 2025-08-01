// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

package rcscrape

import (
	"cmp"
	"slices"
	"time"

	"github.com/DataDog/datadog-agent/pkg/dyninst/actuator"
	"github.com/DataDog/datadog-agent/pkg/dyninst/ir"
	"github.com/DataDog/datadog-agent/pkg/dyninst/procmon"
	"github.com/DataDog/datadog-agent/pkg/dyninst/rcjson"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// debouncer tracks messages from the dd-trace-go callback and coalesces them
// into a single update for a process. We have to do this because there is
// no sentinel message when the iteration through the configs begins or ends.
// Instead, we rely on the idle period to determine when we've seen all the
// configs for a process.
type debouncer struct {
	idlePeriod time.Duration
	processes  map[actuator.ProcessID]*debouncerProcess
}

func makeDebouncer(idlePeriod time.Duration) debouncer {
	return debouncer{
		idlePeriod: idlePeriod,
		processes:  make(map[actuator.ProcessID]*debouncerProcess),
	}
}

type debouncerProcess struct {
	procmon.ProcessUpdate
	runtimeID    string
	lastUpdated  time.Time
	files        []remoteConfigFile
	symdbEnabled bool
}

func (c *debouncer) track(
	proc procmon.ProcessUpdate,
) {
	c.processes[proc.ProcessID] = &debouncerProcess{
		ProcessUpdate: proc,
	}
}

func (c *debouncer) untrack(processID actuator.ProcessID) {
	delete(c.processes, processID)
}

func (c *debouncer) addSymdbEnabled(
	now time.Time,
	processID actuator.ProcessID,
	runtimeID string,
	symdbEnabled bool,
) {
	p, ok := c.processes[processID]
	if !ok {
		// Update corresponds to an untracked process.
		return
	}
	p.lastUpdated = now
	if p.runtimeID != "" && p.runtimeID != runtimeID {
		log.Warnf(
			"rcscrape: process %v: runtime ID mismatch: %s != %s",
			p.ProcessID, p.runtimeID, runtimeID,
		)
	}
	p.runtimeID = runtimeID
	p.symdbEnabled = symdbEnabled
	if log.ShouldLog(log.TraceLvl) {
		log.Tracef(
			"rcscrape: process %v: symdb enabled: %t",
			p.ProcessID, symdbEnabled,
		)
	}
}

func (c *debouncer) addInFlight(
	now time.Time,
	processID actuator.ProcessID,
	file remoteConfigFile,
) {
	p, ok := c.processes[processID]
	if !ok {
		// Update corresponds to an untracked process.
		return
	}
	p.lastUpdated = now
	if p.runtimeID != "" && p.runtimeID != file.RuntimeID {
		log.Warnf(
			"rcscrape: process %v: runtime ID mismatch: %s != %s",
			p.ProcessID, p.runtimeID, file.RuntimeID,
		)
		clear(p.files)
	}
	p.runtimeID = file.RuntimeID
	if file.ConfigContent == "" {
		return
	}
	p.files = append(p.files, file)
	if log.ShouldLog(log.TraceLvl) {
		log.Tracef(
			"rcscrape: process %v: got update for %s",
			p.ProcessID, file.ConfigPath,
		)
	}
}

func (c *debouncer) coalesceInFlight(now time.Time) []ProcessUpdate {
	var updates []ProcessUpdate

	for procID, process := range c.processes {
		if process.lastUpdated.IsZero() ||
			process.lastUpdated.Add(c.idlePeriod).After(now) {
			continue
		}
		probes := computeProbeDefinitions(procID, process.files)
		process.files, process.lastUpdated = nil, time.Time{}
		updates = append(updates, ProcessUpdate{
			ProcessUpdate:     process.ProcessUpdate,
			Probes:            probes,
			RuntimeID:         process.runtimeID,
			ShouldUploadSymDB: process.symdbEnabled,
		})
	}
	slices.SortFunc(updates, func(a, b ProcessUpdate) int {
		return cmp.Compare(a.ProcessID.PID, b.ProcessID.PID)
	})
	return updates
}

func computeProbeDefinitions(
	procID actuator.ProcessID,
	files []remoteConfigFile,
) []ir.ProbeDefinition {
	slices.SortFunc(files, func(a, b remoteConfigFile) int {
		return cmp.Compare(a.ConfigPath, b.ConfigPath)
	})
	files = slices.CompactFunc(files, sameConfigPath)
	probes := make([]ir.ProbeDefinition, 0, len(files))
	for _, file := range files {
		// TODO: Optimize away this copy of the underlying data by either
		// using unsafe or changing rcjson to use an io.Reader and reusing
		// a strings.Reader.
		probe, err := rcjson.UnmarshalProbe([]byte(file.ConfigContent))
		if err != nil {
			// TODO: Rate limit this warning in some form.
			log.Warnf(
				"process %v: failed to unmarshal probe %s: %v",
				procID, file.ConfigPath, err,
			)
			continue
		}
		probes = append(probes, probe)
	}
	// Collapse duplicates if they somehow showed up.
	slices.SortFunc(probes, ir.CompareProbeIDs)
	probes = slices.CompactFunc(probes, eqProbeIDs)
	return probes
}

func sameConfigPath(a, b remoteConfigFile) bool {
	return a.ConfigPath == b.ConfigPath
}

func eqProbeIDs[A, B ir.ProbeIDer](a A, b B) bool {
	return ir.CompareProbeIDs(a, b) == 0
}
