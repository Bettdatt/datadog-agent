// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package checks

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/config/env"
	pkgconfigmodel "github.com/DataDog/datadog-agent/pkg/config/model"
	"github.com/DataDog/datadog-agent/pkg/util/flavor"
	"github.com/DataDog/datadog-agent/pkg/util/log"

	model "github.com/DataDog/agent-payload/v5/process"

	"github.com/DataDog/datadog-agent/pkg/process/procutil"
)

// NewProcessDiscoveryCheck returns an instance of the ProcessDiscoveryCheck.
func NewProcessDiscoveryCheck(config pkgconfigmodel.Reader) *ProcessDiscoveryCheck {
	return &ProcessDiscoveryCheck{
		config:    config,
		scrubber:  procutil.NewDefaultDataScrubber(),
		userProbe: NewLookupIDProbe(config),
	}
}

// ProcessDiscoveryCheck is a check that gathers basic process metadata.
// It uses its own ProcessDiscovery payload.
// The goal of this check is to collect information about possible integrations that may be enabled by the end user.
type ProcessDiscoveryCheck struct {
	config pkgconfigmodel.Reader

	probe      procutil.Probe
	scrubber   *procutil.DataScrubber
	userProbe  *LookupIdProbe
	info       *HostInfo
	initCalled bool

	maxBatchSize int
}

// Init initializes the ProcessDiscoveryCheck. It is a runtime error to call Run without first having called Init.
func (d *ProcessDiscoveryCheck) Init(syscfg *SysProbeConfig, info *HostInfo, _ bool) error {
	d.info = info
	d.initCalled = true
	initScrubber(d.config, d.scrubber)
	d.probe = newProcessProbe(d.config, procutil.WithPermission(syscfg.ProcessModuleEnabled))

	d.maxBatchSize = getMaxBatchSize(d.config)
	return nil
}

// IsEnabled returns true if the check is enabled by configuration
func (d *ProcessDiscoveryCheck) IsEnabled() bool {
	if d.config.GetBool("process_config.run_in_core_agent.enabled") && flavor.GetFlavor() == flavor.ProcessAgent {
		return false
	}

	// The Process and Process Discovery checks are mutually exclusive
	if d.config.GetBool("process_config.process_collection.enabled") {
		return false
	}

	if env.IsECSFargate() {
		log.Debug("Process discovery is not supported on ECS Fargate")
		return false
	}

	return d.config.GetBool("process_config.process_discovery.enabled")
}

// SupportsRunOptions returns true if the check supports RunOptions
func (d *ProcessDiscoveryCheck) SupportsRunOptions() bool {
	return false
}

// Name returns the name of the ProcessDiscoveryCheck.
func (d *ProcessDiscoveryCheck) Name() string { return DiscoveryCheckName }

// Realtime returns a value that says whether this check should be run in real time.
func (d *ProcessDiscoveryCheck) Realtime() bool { return false }

// ShouldSaveLastRun indicates if the output from the last run should be saved for use in flares
func (d *ProcessDiscoveryCheck) ShouldSaveLastRun() bool { return true }

// Run collects process metadata, and packages it into a CollectorProcessDiscovery payload to be sent.
// It is a runtime error to call Run without first having called Init.
func (d *ProcessDiscoveryCheck) Run(nextGroupID func() int32, options *RunOptions) (RunResult, error) {
	if !d.initCalled {
		return nil, fmt.Errorf("ProcessDiscoveryCheck.Run called before Init")
	}

	// Does not need to collect process stats, only metadata
	procs, err := d.probe.ProcessesByPID(time.Now(), false)
	if err != nil {
		return nil, err
	}

	host := &model.Host{
		Name:        d.info.HostName,
		NumCpus:     calculateNumCores(d.info.SystemInfo),
		TotalMemory: d.info.SystemInfo.TotalMemory,
	}

	procDiscoveries := pidMapToProcDiscoveries(procs, d.userProbe, d.scrubber)

	// For no chunking, set max batch size as number of process discoveries to ensure one chunk
	runMaxBatchSize := d.maxBatchSize
	if options != nil && options.NoChunking {
		runMaxBatchSize = len(procDiscoveries)
	}

	procDiscoveryChunks := chunkProcessDiscoveries(procDiscoveries, runMaxBatchSize)
	payload := make([]model.MessageBody, len(procDiscoveryChunks))

	groupID := nextGroupID()
	for i, procDiscoveryChunk := range procDiscoveryChunks {
		payload[i] = &model.CollectorProcDiscovery{
			HostName:           d.info.HostName,
			GroupId:            groupID,
			GroupSize:          int32(len(procDiscoveryChunks)),
			ProcessDiscoveries: procDiscoveryChunk,
			Host:               host,
		}
	}

	return StandardRunResult(payload), nil
}

// Cleanup frees any resource held by the ProcessDiscoveryCheck before the agent exits
func (d *ProcessDiscoveryCheck) Cleanup() {}

func pidMapToProcDiscoveries(pidMap map[int32]*procutil.Process, userProbe *LookupIdProbe, scrubber *procutil.DataScrubber) []*model.ProcessDiscovery {
	pd := make([]*model.ProcessDiscovery, 0, len(pidMap))
	for _, proc := range pidMap {
		proc.Cmdline = scrubber.ScrubProcessCommand(proc)
		pd = append(pd, &model.ProcessDiscovery{
			Pid:        proc.Pid,
			NsPid:      proc.NsPid,
			Command:    formatCommand(proc),
			User:       formatUser(proc, userProbe),
			CreateTime: proc.Stats.CreateTime,
		})
	}

	return pd
}

// chunkProcessDiscoveries split non-container processes into chunks and return a list of chunks
// This function is patiently awaiting go to support generics, so that we don't need two chunkProcesses functions :)
func chunkProcessDiscoveries(procs []*model.ProcessDiscovery, size int) [][]*model.ProcessDiscovery {
	chunkCount := len(procs) / size
	if chunkCount*size < len(procs) {
		chunkCount++
	}
	chunks := make([][]*model.ProcessDiscovery, 0, chunkCount)

	for i := 0; i < len(procs); i += size {
		end := min(i+size, len(procs))
		chunks = append(chunks, procs[i:end])
	}

	return chunks
}

// Needed to calculate the correct normalized cpu metric value
// On linux, the cpu array contains an entry per logical core.
// On windows, the cpu array contains an entry per physical core, with correct logical core counts.
func calculateNumCores(info *model.SystemInfo) (numCores int32) {
	for _, cpu := range info.Cpus {
		numCores += cpu.Cores
	}

	return numCores
}
