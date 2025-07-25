// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package process holds process related files
package process

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DataDog/datadog-go/v5/statsd"
	manager "github.com/DataDog/ebpf-manager"
	lib "github.com/cilium/ebpf"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/shirou/gopsutil/v4/process"
	"go.uber.org/atomic"

	"github.com/DataDog/datadog-agent/pkg/process/procutil"
	"github.com/DataDog/datadog-agent/pkg/security/ebpf"
	"github.com/DataDog/datadog-agent/pkg/security/metrics"
	"github.com/DataDog/datadog-agent/pkg/security/probe/config"
	"github.com/DataDog/datadog-agent/pkg/security/probe/managerhelper"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/cgroup"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/container"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/envvars"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/mount"
	spath "github.com/DataDog/datadog-agent/pkg/security/resolvers/path"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/usergroup"
	"github.com/DataDog/datadog-agent/pkg/security/secl/containerutils"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model"
	"github.com/DataDog/datadog-agent/pkg/security/secl/model/sharedconsts"
	"github.com/DataDog/datadog-agent/pkg/security/seclog"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	stime "github.com/DataDog/datadog-agent/pkg/util/ktime"
)

const (
	Snapshotting = iota // Snapshotting describes the state where resolvers are being populated
	Snapshotted         // Snapshotted describes the state where resolvers are fully populated
)

const (
	procResolveMaxDepth              = 16
	maxParallelArgsEnvs              = 512 // == number of parallel starting processes
	argsEnvsValueCacheSize           = 8192
	numAllowedPIDsToResolvePerPeriod = 1
	procFallbackLimiterPeriod        = 30 * time.Second // proc fallback period by pid
)

// EBPFResolver resolved process context
type EBPFResolver struct {
	sync.RWMutex
	state *atomic.Int64

	manager      *manager.Manager
	config       *config.Config
	statsdClient statsd.ClientInterface
	scrubber     *procutil.DataScrubber

	containerResolver *container.Resolver
	mountResolver     mount.ResolverInterface
	cgroupResolver    *cgroup.Resolver
	userGroupResolver *usergroup.Resolver
	timeResolver      *stime.Resolver
	pathResolver      spath.ResolverInterface
	envVarsResolver   *envvars.Resolver

	inodeFileMap ebpf.Map
	procCacheMap ebpf.Map
	pidCacheMap  ebpf.Map
	opts         ResolverOpts

	// stats
	processCacheEntryCount    *atomic.Int64
	hitsStats                 map[string]*atomic.Int64
	missStats                 *atomic.Int64
	addedEntriesFromEvent     *atomic.Int64
	addedEntriesFromKernelMap *atomic.Int64
	addedEntriesFromProcFS    *atomic.Int64
	flushedEntries            *atomic.Int64
	pathErrStats              *atomic.Int64
	argsTruncated             *atomic.Int64
	argsSize                  *atomic.Int64
	envsTruncated             *atomic.Int64
	envsSize                  *atomic.Int64
	brokenLineage             *atomic.Int64
	inodeErrStats             *atomic.Int64

	entryCache              map[uint32]*model.ProcessCacheEntry
	SnapshottedBoundSockets map[uint32][]model.SnapshottedBoundSocket
	argsEnvsCache           *simplelru.LRU[uint64, *argsEnvsCacheEntry]

	processCacheEntryPool *Pool

	// limiters
	procFallbackLimiter *utils.Limiter[uint32]

	exitedQueue []uint32
}

// DequeueExited dequeue exited process
func (p *EBPFResolver) DequeueExited() {
	p.Lock()
	defer p.Unlock()

	delEntry := func(pid uint32, exitTime time.Time) {
		p.deleteEntry(pid, exitTime)
		p.flushedEntries.Inc()
	}

	now := time.Now()
	for _, pid := range p.exitedQueue {
		entry := p.entryCache[pid]
		if entry == nil {
			continue
		}

		if tm := entry.ExecTime; !tm.IsZero() && tm.Add(time.Minute).Before(now) {
			delEntry(pid, now)
		} else if tm := entry.ForkTime; !tm.IsZero() && tm.Add(time.Minute).Before(now) {
			delEntry(pid, now)
		} else if entry.ForkTime.IsZero() && entry.ExecTime.IsZero() {
			delEntry(pid, now)
		}
	}

	p.exitedQueue = p.exitedQueue[0:0]
}

// NewProcessCacheEntry returns a new process cache entry
func (p *EBPFResolver) NewProcessCacheEntry(pidContext model.PIDContext) *model.ProcessCacheEntry {
	entry := p.processCacheEntryPool.Get()
	entry.PIDContext = pidContext
	entry.Cookie = utils.NewCookie()

	return entry
}

// CountBrokenLineage increments the counter of broken lineage
func (p *EBPFResolver) CountBrokenLineage() {
	p.brokenLineage.Inc()
}

// SendStats sends process resolver metrics
func (p *EBPFResolver) SendStats() error {
	if err := p.statsdClient.Gauge(metrics.MetricProcessResolverCacheSize, p.getEntryCacheSize(), []string{}, 1.0); err != nil {
		return fmt.Errorf("failed to send process_resolver cache_size metric: %w", err)
	}

	if err := p.statsdClient.Gauge(metrics.MetricProcessResolverReferenceCount, p.getProcessCacheEntryCount(), []string{}, 1.0); err != nil {
		return fmt.Errorf("failed to send process_resolver reference_count metric: %w", err)
	}

	for _, resolutionType := range metrics.AllTypesTags {
		if count := p.hitsStats[resolutionType].Swap(0); count > 0 {
			if err := p.statsdClient.Count(metrics.MetricProcessResolverHits, count, []string{resolutionType}, 1.0); err != nil {
				return fmt.Errorf("failed to send process_resolver with `%s` metric: %w", resolutionType, err)
			}
		}
	}

	if count := p.missStats.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverMiss, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver misses metric: %w", err)
		}
	}

	if count := p.addedEntriesFromEvent.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverAdded, count, metrics.ProcessSourceEventTags, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver added entries metric: %w", err)
		}
	}

	if count := p.addedEntriesFromKernelMap.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverAdded, count, metrics.ProcessSourceKernelMapsTags, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver added entries from kernel map metric: %w", err)
		}
	}

	if count := p.addedEntriesFromProcFS.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverAdded, count, metrics.ProcessSourceProcTags, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver added entries from kernel map metric: %w", err)
		}
	}

	if count := p.flushedEntries.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverFlushed, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver flushed entries metric: %w", err)
		}
	}

	if count := p.pathErrStats.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverPathError, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver path error metric: %w", err)
		}
	}

	if count := p.argsTruncated.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverArgsTruncated, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send args truncated metric: %w", err)
		}
	}

	if count := p.argsSize.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverArgsSize, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send args size metric: %w", err)
		}
	}

	if count := p.envsTruncated.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverEnvsTruncated, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send envs truncated metric: %w", err)
		}
	}

	if count := p.envsSize.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessResolverEnvsSize, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send envs size metric: %w", err)
		}
	}

	if count := p.brokenLineage.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessEventBrokenLineage, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver broken lineage metric: %w", err)
		}
	}

	if count := p.inodeErrStats.Swap(0); count > 0 {
		if err := p.statsdClient.Count(metrics.MetricProcessInodeError, count, []string{}, 1.0); err != nil {
			return fmt.Errorf("failed to send process_resolver inode error metric: %w", err)
		}
	}

	return nil
}

type argsEnvsCacheEntry struct {
	values    []string
	truncated bool
}

var argsEnvsInterner = utils.NewLRUStringInterner(argsEnvsValueCacheSize)

func parseStringArray(data []byte) ([]string, bool) {
	truncated := false
	values, err := model.UnmarshalStringArray(data)
	if err != nil || len(data) == sharedconsts.MaxArgEnvSize {
		if len(values) > 0 {
			values[len(values)-1] += "..."
		}
		truncated = true
	}

	argsEnvsInterner.DeduplicateSlice(values)
	return values, truncated
}

func newArgsEnvsCacheEntry(event *model.ArgsEnvsEvent) *argsEnvsCacheEntry {
	values, truncated := parseStringArray(event.ValuesRaw[:event.Size])
	return &argsEnvsCacheEntry{
		values:    values,
		truncated: truncated,
	}
}

func (e *argsEnvsCacheEntry) extend(event *model.ArgsEnvsEvent) {
	values, truncated := parseStringArray(event.ValuesRaw[:event.Size])
	if truncated {
		e.truncated = true
	}
	e.values = append(e.values, values...)
}

// UpdateArgsEnvs updates arguments or environment variables of the given id
func (p *EBPFResolver) UpdateArgsEnvs(event *model.ArgsEnvsEvent) {
	if list, found := p.argsEnvsCache.Get(event.ID); found {
		list.extend(event)
	} else {
		p.argsEnvsCache.Add(event.ID, newArgsEnvsCacheEntry(event))
	}
}

// AddForkEntry adds an entry to the local cache and returns the newly created entry
func (p *EBPFResolver) AddForkEntry(event *model.Event, newEntryCb func(*model.ProcessCacheEntry, error)) error {
	p.ApplyBootTime(event.ProcessCacheEntry)
	event.ProcessCacheEntry.SetSpan(event.SpanContext.SpanID, event.SpanContext.TraceID)

	if event.ProcessCacheEntry.Pid == 0 {
		return errors.New("no pid")
	}
	if IsKThread(event.ProcessCacheEntry.PPid, event.ProcessCacheEntry.Pid) {
		return errors.New("process is kthread")
	}

	p.Lock()
	defer p.Unlock()
	p.insertForkEntry(event.ProcessCacheEntry, event.PIDContext.ExecInode, model.ProcessCacheEntryFromEvent, newEntryCb)
	return nil
}

// AddExecEntry adds an entry to the local cache and returns the newly created entry
func (p *EBPFResolver) AddExecEntry(event *model.Event) error {
	p.Lock()
	defer p.Unlock()

	var err error
	if err := p.ResolveNewProcessCacheEntry(event.ProcessCacheEntry, event.ContainerContext); err != nil {
		var errResolution *spath.ErrPathResolution
		if errors.As(err, &errResolution) {
			event.SetPathResolutionError(&event.ProcessCacheEntry.FileEvent, err)
		}
	} else {
		if event.ProcessCacheEntry.Pid == 0 {
			return errors.New("no pid context")
		}
		p.insertExecEntry(event.ProcessCacheEntry, event.PIDContext.ExecInode, model.ProcessCacheEntryFromEvent)
	}

	event.Exec.Process = &event.ProcessCacheEntry.Process

	return err
}

// ApplyExitEntry delete entry from the local cache if present
func (p *EBPFResolver) ApplyExitEntry(event *model.Event, newEntryCb func(*model.ProcessCacheEntry, error)) bool {
	event.ProcessCacheEntry = p.resolve(event.PIDContext.Pid, event.PIDContext.Tid, event.PIDContext.ExecInode, false, newEntryCb)
	if event.ProcessCacheEntry == nil {
		// no need to dispatch an exit event that don't have the corresponding cache entry
		return false
	}

	// Use the event timestamp as exit time
	// The local process cache hasn't been updated yet with the exit time when the exit event is first seen
	// The pid_cache kernel map has the exit_time but it's only accessed if there's a local miss
	event.ProcessCacheEntry.ExitTime = event.FieldHandlers.ResolveEventTime(event, &event.BaseEvent)
	event.Exit.Process = &event.ProcessCacheEntry.Process
	return true

}

// enrichEventFromProcfs uses /proc to enrich a ProcessCacheEntry with additional metadata
func (p *EBPFResolver) enrichEventFromProcfs(entry *model.ProcessCacheEntry, proc *process.Process, filledProc *utils.FilledProcess) error {
	// the provided process is a kernel process if its virtual memory size is null
	if filledProc.MemInfo.VMS == 0 {
		return fmt.Errorf("cannot snapshot kernel threads")
	}
	pid := uint32(proc.Pid)

	// Get process filename and pre-fill the cache
	procExecPath := utils.ProcExePath(pid)
	pathnameStr, err := os.Readlink(procExecPath)
	if err != nil {
		return fmt.Errorf("snapshot failed for %d: couldn't readlink binary: %w", proc.Pid, err)
	}
	if pathnameStr == "/ (deleted)" {
		return fmt.Errorf("snapshot failed for %d: binary was deleted", proc.Pid)
	}

	// Get the file fields of the process binary
	info, err := p.RetrieveFileFieldsFromProcfs(procExecPath)
	if err != nil {
		if !os.IsNotExist(err) {
			seclog.Errorf("snapshot failed for %d: couldn't retrieve file info: %s", proc.Pid, err)
		}
		return fmt.Errorf("snapshot failed for %d: couldn't retrieve file info: %w", proc.Pid, err)
	}

	// Retrieve the container ID of the process from /proc and /sys/fs/cgroup/[cgroup]
	containerID, cgroup, cgroupPath, err := p.containerResolver.GetContainerContext(pid)
	if err != nil {
		errMsg := fmt.Sprintf("snapshot failed for %d: couldn't parse container and cgroup context: %s", proc.Pid, err)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			// If the process is not found, it may have exited, so we log a warning
			seclog.Warnf("%s", errMsg)
		} else {
			seclog.Errorf("%s", errMsg)
		}
	} else if cgroup.CGroupFile.Inode != 0 && cgroup.CGroupFile.MountID == 0 { // the mount id is unavailable through statx
		// Get the file fields of the sysfs cgroup file
		info, err := p.RetrieveFileFieldsFromProcfs(cgroupPath)
		if err != nil {
			seclog.Warnf("snapshot failed for %d: couldn't retrieve file info: %s", proc.Pid, err)
		} else {
			cgroup.CGroupFile.MountID = info.MountID
		}
	}

	entry.ContainerID = containerID

	entry.CGroup = cgroup
	entry.Process.CGroup = cgroup

	entry.FileEvent.FileFields = *info
	setPathname(&entry.FileEvent, pathnameStr)

	// force mount from procfs/snapshot
	entry.FileEvent.MountOrigin = model.MountOriginProcfs
	entry.FileEvent.MountSource = model.MountSourceSnapshot
	entry.FileEvent.MountVisibilityResolved = true

	if entry.FileEvent.IsFileless() {
		entry.FileEvent.MountVisible = false
		entry.FileEvent.MountDetached = true
		entry.FileEvent.Filesystem = model.TmpFS
	} else {
		entry.FileEvent.MountVisible = true
		entry.FileEvent.MountDetached = false
		// resolve container path with the MountEBPFResolver
		entry.FileEvent.Filesystem, err = p.mountResolver.ResolveFilesystem(entry.Process.FileEvent.MountID, entry.Process.FileEvent.Device, entry.Process.Pid, containerID)
		if err != nil {
			seclog.Debugf("snapshot failed for mount %d with pid %d : couldn't get the filesystem: %s", entry.Process.FileEvent.MountID, proc.Pid, err)
		}
	}

	entry.ExecTime = time.Unix(0, filledProc.CreateTime*int64(time.Millisecond))
	entry.ForkTime = entry.ExecTime
	entry.Comm = filledProc.Name
	entry.PPid = uint32(filledProc.Ppid)
	entry.TTYName = utils.PidTTY(uint32(filledProc.Pid))
	entry.ProcessContext.Pid = pid
	entry.ProcessContext.Tid = pid
	if len(filledProc.Uids) >= 4 {
		entry.Credentials.UID = filledProc.Uids[0]
		entry.Credentials.EUID = filledProc.Uids[1]
		entry.Credentials.FSUID = filledProc.Uids[3]
	}
	if len(filledProc.Gids) >= 4 {
		entry.Credentials.GID = filledProc.Gids[0]
		entry.Credentials.EGID = filledProc.Gids[1]
		entry.Credentials.FSGID = filledProc.Gids[3]
	}
	// fetch login_uid
	entry.Credentials.AUID, err = utils.GetLoginUID(uint32(proc.Pid))
	if err != nil {
		return fmt.Errorf("snapshot failed for %d: couldn't get login UID: %w", proc.Pid, err)
	}

	entry.Credentials.CapEffective, entry.Credentials.CapPermitted, err = utils.CapEffCapEprm(uint32(proc.Pid))
	if err != nil {
		return fmt.Errorf("snapshot failed for %d: couldn't parse kernel capabilities: %w", proc.Pid, err)
	}
	p.SetProcessUsersGroups(entry)

	// args and envs
	entry.ArgsEntry = &model.ArgsEntry{}
	if len(filledProc.Cmdline) > 0 {
		entry.ArgsEntry.Values = filledProc.Cmdline
	}

	entry.EnvsEntry = &model.EnvsEntry{}
	if envs, truncated, err := p.envVarsResolver.ResolveEnvVars(uint32(proc.Pid)); err == nil {
		entry.EnvsEntry.Values = envs
		entry.EnvsEntry.Truncated = truncated
	}

	// Heuristic to detect likely interpreter event
	// Cannot detect when a script if as follows:
	// perl <<__HERE__
	// #!/usr/bin/perl
	//
	// sleep 10;
	//
	// print "Hello from Perl\n";
	// __HERE__
	// Because the entry only has 1 argument (perl in this case). But can detect when a script is as follows:
	// cat << EOF > perlscript.pl
	// #!/usr/bin/perl
	//
	// sleep 15;
	//
	// print "Hello from Perl\n";
	//
	// EOF
	if values := entry.ArgsEntry.Values; len(values) > 1 {
		firstArg := values[0]
		lastArg := values[len(values)-1]
		// Example result: comm value: pyscript.py | args: [/usr/bin/python3 ./pyscript.py]
		if path.Base(lastArg) == entry.Comm && path.IsAbs(firstArg) {
			entry.LinuxBinprm.FileEvent = entry.FileEvent
		}
	}

	if !entry.HasInterpreter() {
		// mark it as resolved to avoid abnormal path later in the call flow
		entry.LinuxBinprm.FileEvent.SetPathnameStr("")
		entry.LinuxBinprm.FileEvent.SetBasenameStr("")
	}

	// add netns
	entry.NetNS, _ = utils.NetNSPathFromPid(pid).GetProcessNetworkNamespace()

	if p.config.NetworkEnabled {
		// snapshot pid routes in kernel space
		_, _ = proc.OpenFiles()
	}

	return nil
}

func (p *EBPFResolver) statFile(filename string) (uint64, []byte, error) {
	// first stat to reserve the entry in the map and let the second stat update the entry
	stat, err := utils.UnixStat(filename)
	if err != nil {
		return 0, nil, err
	}

	inodeb := make([]byte, 8)
	binary.NativeEndian.PutUint64(inodeb, stat.Ino)

	// push to allocate the entry
	fileFields := model.FileFields{
		PathKey: model.PathKey{
			Inode: stat.Ino,
		},
	}

	data := make([]byte, model.FileFieldsSize)
	if _, err = fileFields.MarshalBinary(data); err != nil {
		return 0, inodeb, err
	}

	if err = p.inodeFileMap.Put(inodeb, data); err != nil {
		return 0, nil, err
	}

	// stat again to let the kernel part update the entry
	if _, err = utils.UnixStat(filename); err != nil {
		return 0, nil, err
	}

	return stat.Ino, inodeb, nil
}

// RetrieveFileFieldsFromProcfs fetches inode metadata from kernel space.
// stat the file which triggers the security_inode_getattr, which fill a map with the needed data
func (p *EBPFResolver) RetrieveFileFieldsFromProcfs(filename string) (*model.FileFields, error) {
	inode, inodeb, err := p.statFile(filename)
	if err != nil {
		return nil, err
	}

	data, err := p.inodeFileMap.LookupBytes(inodeb)
	// go back to a sane error value
	if data == nil && err == nil {
		err = lib.ErrKeyNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("unable to get filename for inode `%d`: %w", inode, err)
	}

	// free the slot
	_ = p.inodeFileMap.Delete(inodeb)

	var fileFields model.FileFields
	if _, err := fileFields.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("unable to unmarshal entry for inode `%d`: %w", inode, err)
	}

	if fileFields.Inode == 0 {
		return nil, fmt.Errorf("inode `%d` not found", inode)
	}

	return &fileFields, nil
}

func (p *EBPFResolver) insertEntry(entry *model.ProcessCacheEntry, source uint64) {
	entry.Source = source

	if prev := p.entryCache[entry.Pid]; prev != nil {
		prev.Release()
	}

	p.entryCache[entry.Pid] = entry
	entry.Retain()
	// only increment the cache entry count when we first retain the entry,
	// the count will be decremented once the entry is released
	p.processCacheEntryCount.Inc()

	if p.cgroupResolver != nil && entry.CGroup.CGroupID != "" {
		// add the new PID in the right cgroup_resolver bucket
		p.cgroupResolver.AddPID(entry)
	}

	switch source {
	case model.ProcessCacheEntryFromEvent:
		p.addedEntriesFromEvent.Inc()
	case model.ProcessCacheEntryFromKernelMap:
		p.addedEntriesFromKernelMap.Inc()
	case model.ProcessCacheEntryFromProcFS:
		p.addedEntriesFromProcFS.Inc()
	}
}

func (p *EBPFResolver) insertForkEntry(entry *model.ProcessCacheEntry, inode uint64, source uint64, newEntryCb func(*model.ProcessCacheEntry, error)) {
	if entry.Pid == 0 {
		return
	}

	prev := p.entryCache[entry.Pid]
	if prev != nil {
		// this shouldn't happen but it is better to exit the prev and let the new one replace it
		prev.Exit(entry.ForkTime)
	}
	if entry.Pid != 1 {
		parent := p.entryCache[entry.PPid]
		if entry.PPid >= 1 && inode != 0 && (parent == nil || parent.FileEvent.Inode != inode) {
			if candidate := p.resolve(entry.PPid, entry.PPid, inode, true, newEntryCb); candidate != nil {
				parent = candidate
			} else {
				entry.IsParentMissing = true
				p.inodeErrStats.Inc()
			}
		}

		if parent != nil {
			parent.Fork(entry)
		} else {
			entry.IsParentMissing = true
		}
	}

	p.insertEntry(entry, source)
}

func (p *EBPFResolver) insertExecEntry(entry *model.ProcessCacheEntry, inode uint64, source uint64) {
	if entry.Pid == 0 {
		return
	}

	prev := p.entryCache[entry.Pid]
	if prev != nil {
		if inode != 0 && prev.FileEvent.Inode != inode {
			entry.IsParentMissing = true
			p.inodeErrStats.Inc()
		}

		// check exec bomb, keep the prev entry and update it
		if prev.Equals(entry) {
			prev.ApplyExecTimeOf(entry)
			return
		}
		prev.Exec(entry)
	} else {
		entry.IsParentMissing = true
	}

	p.insertEntry(entry, source)
}

func (p *EBPFResolver) deleteEntry(pid uint32, exitTime time.Time) {
	// Start by updating the exit timestamp of the pid cache entry
	entry, ok := p.entryCache[pid]
	if !ok {
		return
	}

	if p.cgroupResolver != nil {
		p.cgroupResolver.DelPID(entry.Pid)
	}

	entry.Exit(exitTime)
	delete(p.entryCache, entry.Pid)
	entry.Release()
}

// DeleteEntry tries to delete an entry in the process cache
func (p *EBPFResolver) DeleteEntry(pid uint32, exitTime time.Time) {
	p.Lock()
	defer p.Unlock()

	p.deleteEntry(pid, exitTime)
}

// Resolve returns the cache entry for the given pid
func (p *EBPFResolver) Resolve(pid, tid uint32, inode uint64, useProcFS bool, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	if pid == 0 {
		return nil
	}

	p.Lock()
	defer p.Unlock()

	return p.resolve(pid, tid, inode, useProcFS, newEntryCb)
}

func (p *EBPFResolver) resolve(pid, tid uint32, inode uint64, useProcFS bool, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	if entry := p.resolveFromCache(pid, tid, inode); entry != nil {
		p.hitsStats[metrics.CacheTag].Inc()
		return entry
	}

	if p.state.Load() != Snapshotted {
		return nil
	}

	// fallback to the kernel maps directly, the perf event may be delayed / may have been lost
	if entry := p.resolveFromKernelMaps(pid, tid, inode, newEntryCb); entry != nil {
		p.hitsStats[metrics.KernelMapsTag].Inc()
		return entry
	}

	if !useProcFS {
		p.missStats.Inc()
		return nil
	}

	if p.procFallbackLimiter.Allow(pid) {
		// fallback to /proc, the in-kernel LRU may have deleted the entry
		if entry := p.resolveFromProcfs(pid, inode, procResolveMaxDepth, newEntryCb); entry != nil {
			p.hitsStats[metrics.ProcFSTag].Inc()
			return entry
		}
	}

	p.missStats.Inc()
	return nil
}

func (p *EBPFResolver) resolveFileFieldsPath(e *model.FileFields, pce *model.ProcessCacheEntry, ctrCtx *model.ContainerContext) (string, string, model.MountSource, model.MountOrigin, error) {
	var (
		pathnameStr, mountPath string
		source                 model.MountSource
		origin                 model.MountOrigin
		err                    error
		maxDepthRetry          = 3
	)
	for maxDepthRetry > 0 {
		pathnameStr, mountPath, source, origin, err = p.pathResolver.ResolveFileFieldsPath(e, &pce.PIDContext, ctrCtx)
		if err == nil {
			return pathnameStr, mountPath, source, origin, nil
		}
		parent, exists := p.entryCache[pce.PPid]
		if !exists {
			break
		}

		pce = parent
		maxDepthRetry--
	}

	return pathnameStr, mountPath, source, origin, err
}

// SetProcessPath resolves process file path
func (p *EBPFResolver) SetProcessPath(fileEvent *model.FileEvent, pce *model.ProcessCacheEntry, ctrCtx *model.ContainerContext) (string, error) {
	onError := func(pathnameStr string, err error) (string, error) {
		fileEvent.SetPathnameStr("")
		fileEvent.SetBasenameStr("")

		p.pathErrStats.Inc()

		return pathnameStr, err
	}

	if fileEvent.Inode == 0 {
		return onError("", &model.ErrInvalidKeyPath{Inode: fileEvent.Inode, MountID: fileEvent.MountID})
	}
	pathnameStr, mountPath, source, origin, err := p.resolveFileFieldsPath(&fileEvent.FileFields, pce, ctrCtx)
	if err != nil {
		return onError(pathnameStr, err)
	}
	setPathname(fileEvent, pathnameStr)
	fileEvent.MountPath = mountPath
	fileEvent.MountSource = source
	fileEvent.MountOrigin = origin
	err = p.pathResolver.ResolveMountAttributes(fileEvent, &pce.PIDContext, ctrCtx)
	if err != nil {
		seclog.Warnf("Failed to resolve mount attributes for mount id %d: %s", fileEvent.MountID, err)
	}

	return fileEvent.PathnameStr, nil
}

// SetProcessSymlink resolves process file symlink path
func (p *EBPFResolver) SetProcessSymlink(entry *model.ProcessCacheEntry) {
	// TODO: busybox workaround only for now
	if IsBusybox(entry.FileEvent.PathnameStr) {
		arg0, _ := GetProcessArgv0(&entry.Process)
		base := path.Base(arg0)

		entry.SymlinkPathnameStr[0] = "/bin/" + base
		entry.SymlinkPathnameStr[1] = "/usr/bin/" + base

		entry.SymlinkBasenameStr = base
	}
}

// SetProcessFilesystem resolves process file system
func (p *EBPFResolver) SetProcessFilesystem(entry *model.ProcessCacheEntry) (string, error) {
	if entry.FileEvent.MountID != 0 {
		fs, err := p.mountResolver.ResolveFilesystem(entry.FileEvent.MountID, entry.FileEvent.Device, entry.Pid, entry.ContainerID)
		if err != nil {
			return "", err
		}
		entry.FileEvent.Filesystem = fs
	}

	return entry.FileEvent.Filesystem, nil
}

// ApplyBootTime realign timestamp from the boot time
func (p *EBPFResolver) ApplyBootTime(entry *model.ProcessCacheEntry) {
	entry.ExecTime = p.timeResolver.ApplyBootTime(entry.ExecTime)
	entry.ForkTime = p.timeResolver.ApplyBootTime(entry.ForkTime)
	entry.ExitTime = p.timeResolver.ApplyBootTime(entry.ExitTime)
}

// ResolveFromCache resolves cache entry from the cache
func (p *EBPFResolver) ResolveFromCache(pid, tid uint32, inode uint64) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()
	return p.resolveFromCache(pid, tid, inode)
}

func (p *EBPFResolver) resolveFromCache(pid, tid uint32, inode uint64) *model.ProcessCacheEntry {
	entry, exists := p.entryCache[pid]
	if !exists {
		return nil
	}

	// Compare inode to ensure that the cache is up-to-date.
	// Be sure to compare with the file inode and not the pidcontext which can be empty
	// if the entry originates from procfs.
	if inode != 0 && inode != entry.Process.FileEvent.Inode {
		return nil
	}

	// make to update the tid with the that triggers the resolution
	entry.Tid = tid

	return entry
}

// ResolveNewProcessCacheEntry resolves the context fields of a new process cache entry parsed from kernel data
func (p *EBPFResolver) ResolveNewProcessCacheEntry(entry *model.ProcessCacheEntry, ctrCtx *model.ContainerContext) error {
	if _, err := p.SetProcessPath(&entry.FileEvent, entry, ctrCtx); err != nil {
		return &spath.ErrPathResolution{Err: fmt.Errorf("failed to resolve exec path: %w", err)}
	}

	if entry.HasInterpreter() {
		if _, err := p.SetProcessPath(&entry.LinuxBinprm.FileEvent, entry, ctrCtx); err != nil {
			return &spath.ErrPathResolution{Err: fmt.Errorf("failed to resolve interpreter path: %w", err)}
		}
	} else {
		// mark it as resolved to avoid abnormal path later in the call flow
		entry.LinuxBinprm.FileEvent.SetPathnameStr("")
		entry.LinuxBinprm.FileEvent.SetBasenameStr("")
	}

	p.SetProcessArgs(entry)
	p.SetProcessEnvs(entry)
	p.SetProcessTTY(entry)
	p.SetProcessUsersGroups(entry)
	p.ApplyBootTime(entry)
	p.SetProcessSymlink(entry)

	_, err := p.SetProcessFilesystem(entry)

	return err
}

// ResolveFromKernelMaps resolves the entry from the kernel maps
func (p *EBPFResolver) ResolveFromKernelMaps(pid, tid uint32, inode uint64, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()
	return p.resolveFromKernelMaps(pid, tid, inode, newEntryCb)
}

func (p *EBPFResolver) resolveFromKernelMaps(pid, tid uint32, inode uint64, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	if pid == 0 {
		return nil
	}

	pidb := make([]byte, 4)
	binary.NativeEndian.PutUint32(pidb, pid)

	pidCache, err := p.pidCacheMap.LookupBytes(pidb)
	if err != nil {
		// LookupBytes doesn't return an error if the key is not found thus it is a critical error
		seclog.Errorf("kernel map lookup error: %v", err)
	}
	if pidCache == nil {
		return nil
	}

	// first 4 bytes are the actual cookie
	procCache, err := p.procCacheMap.LookupBytes(pidCache[0:model.SizeOfCookie])
	if err != nil {
		// LookupBytes doesn't return an error if the key is not found thus it is a critical error
		seclog.Errorf("kernel map lookup error: %v", err)
	}
	if procCache == nil {
		return nil
	}

	entry := p.NewProcessCacheEntry(model.PIDContext{Pid: pid, Tid: tid, ExecInode: inode})

	var ctrCtx model.ContainerContext
	read, err := ctrCtx.UnmarshalBinary(procCache)
	if err != nil {
		return nil
	}

	cgroupRead, err := entry.CGroup.UnmarshalBinary(procCache)
	if err != nil {
		return nil
	}

	if _, err := entry.UnmarshalProcEntryBinary(procCache[read+cgroupRead:]); err != nil {
		return nil
	}

	// check that the cache entry correspond to the event
	if entry.FileEvent.Inode != 0 && entry.FileEvent.Inode != entry.ExecInode {
		return nil
	}

	if _, err := entry.UnmarshalPidCacheBinary(pidCache); err != nil {
		return nil
	}

	// If we fall back to the kernel maps for a process in a container that was already running when the agent
	// started, the kernel space container ID will be empty even though the process is inside a container. Since there
	// is no insurance that the parent of this process is still running, we can't use our user space cache to check if
	// the parent is in a container. In other words, we have to fall back to /proc to query the container ID of the
	// process.
	if entry.CGroup.CGroupFile.Inode == 0 {
		if containerID, cgroup, _, err := p.containerResolver.GetContainerContext(pid); err == nil {
			entry.CGroup.Merge(&cgroup)
			entry.ContainerID = containerID
		}
	}

	// resolve paths and other context fields
	if err = p.ResolveNewProcessCacheEntry(entry, &ctrCtx); err != nil {
		if newEntryCb != nil {
			newEntryCb(entry, err)
		}

		return nil
	}

	if entry.ExecTime.IsZero() {
		p.insertForkEntry(entry, entry.FileEvent.Inode, model.ProcessCacheEntryFromKernelMap, newEntryCb)
	} else {
		p.insertExecEntry(entry, 0, model.ProcessCacheEntryFromKernelMap)
	}

	if newEntryCb != nil {
		newEntryCb(entry, nil)
	}

	return entry
}

// ResolveFromProcfs resolves the entry from procfs
func (p *EBPFResolver) ResolveFromProcfs(pid uint32, inode uint64, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()
	return p.resolveFromProcfs(pid, inode, procResolveMaxDepth, newEntryCb)
}

func (p *EBPFResolver) resolveFromProcfs(pid uint32, inode uint64, maxDepth int, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	if maxDepth < 1 {
		seclog.Tracef("max depth reached during procfs resolution: %d", pid)
		return nil
	}

	if pid == 0 {
		seclog.Tracef("no pid: %d", pid)
		return nil
	}

	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		seclog.Tracef("unable to find pid: %d", pid)
		return nil
	}

	filledProc, err := utils.GetFilledProcess(proc)
	if err != nil {
		seclog.Tracef("unable to get a filled process for pid %d: %d", pid, err)
		return nil
	}

	// ignore kthreads
	if IsKThread(uint32(filledProc.Ppid), uint32(filledProc.Pid)) {
		return nil
	}

	ppid := uint32(filledProc.Ppid)
	if ppid != 0 && p.entryCache[ppid] == nil {
		// do not use the inode from the pid context on the parent
		// it may be a different process
		p.resolveFromProcfs(ppid, 0, maxDepth-1, newEntryCb)
	}

	return p.newEntryFromProcfs(proc, filledProc, inode, model.ProcessCacheEntryFromProcFS, newEntryCb)
}

// SetProcessArgs set arguments to cache entry
func (p *EBPFResolver) SetProcessArgs(pce *model.ProcessCacheEntry) {
	if entry, found := p.argsEnvsCache.Get(pce.ArgsID); found {
		if pce.ArgsTruncated {
			p.argsTruncated.Inc()
		}

		p.argsSize.Add(int64(len(entry.values)))

		pce.ArgsEntry = &model.ArgsEntry{
			Values:    entry.values,
			Truncated: entry.truncated,
		}

		// no need to keep it in LRU now as attached to a process
		p.argsEnvsCache.Remove(pce.ArgsID)
	}
}

// GetProcessArgvScrubbed returns the scrubbed args of the event as an array
func (p *EBPFResolver) GetProcessArgvScrubbed(pr *model.Process) ([]string, bool) {
	if pr.ArgsEntry == nil || pr.ScrubbedArgvResolved {
		return pr.Argv, pr.ArgsTruncated
	}

	if p.scrubber != nil && len(pr.ArgsEntry.Values) > 0 {
		// replace with the scrubbed version
		argv, _ := p.scrubber.ScrubCommand(pr.ArgsEntry.Values[1:])
		pr.ArgsEntry.Values = []string{pr.ArgsEntry.Values[0]}
		pr.ArgsEntry.Values = append(pr.ArgsEntry.Values, argv...)
	}
	pr.ScrubbedArgvResolved = true

	return GetProcessArgv(pr)
}

// SetProcessEnvs set envs to cache entry
func (p *EBPFResolver) SetProcessEnvs(pce *model.ProcessCacheEntry) {
	if !p.opts.envsResolutionEnabled {
		return
	}

	if entry, found := p.argsEnvsCache.Get(pce.EnvsID); found {
		if pce.EnvsTruncated {
			p.envsTruncated.Inc()
		}

		p.envsSize.Add(int64(len(entry.values)))

		pce.EnvsEntry = &model.EnvsEntry{
			Values:    entry.values,
			Truncated: entry.truncated,
		}

		// no need to keep it in LRU now as attached to a process
		p.argsEnvsCache.Remove(pce.EnvsID)
	}
}

// GetProcessEnvs returns the envs of the event
func (p *EBPFResolver) GetProcessEnvs(pr *model.Process) ([]string, bool) {
	if pr.EnvsEntry == nil {
		return pr.Envs, pr.EnvsTruncated
	}

	keys, truncated := pr.EnvsEntry.FilterEnvs(p.opts.envsWithValue)
	pr.Envs = keys
	pr.EnvsTruncated = pr.EnvsTruncated || truncated
	return pr.Envs, pr.EnvsTruncated
}

// GetProcessEnvp returns the unscrubbed envs of the event with their values. Use with caution.
func (p *EBPFResolver) GetProcessEnvp(pr *model.Process) ([]string, bool) {
	if pr.EnvsEntry == nil {
		return pr.Envp, pr.EnvsTruncated
	}

	pr.Envp = pr.EnvsEntry.Values
	pr.EnvsTruncated = pr.EnvsTruncated || pr.EnvsEntry.Truncated
	return pr.Envp, pr.EnvsTruncated
}

// SetProcessTTY resolves TTY and cache the result
func (p *EBPFResolver) SetProcessTTY(pce *model.ProcessCacheEntry) string {
	if pce.TTYName == "" && p.opts.ttyFallbackEnabled {
		tty := utils.PidTTY(pce.Pid)
		pce.TTYName = tty
	}
	return pce.TTYName
}

// SetProcessUsersGroups resolves and set users and groups
func (p *EBPFResolver) SetProcessUsersGroups(pce *model.ProcessCacheEntry) {
	pce.User, _ = p.userGroupResolver.ResolveUser(int(pce.Credentials.UID), pce.ContainerID)
	pce.EUser, _ = p.userGroupResolver.ResolveUser(int(pce.Credentials.EUID), pce.ContainerID)
	pce.FSUser, _ = p.userGroupResolver.ResolveUser(int(pce.Credentials.FSUID), pce.ContainerID)

	pce.Group, _ = p.userGroupResolver.ResolveGroup(int(pce.Credentials.GID), pce.ContainerID)
	pce.EGroup, _ = p.userGroupResolver.ResolveGroup(int(pce.Credentials.EGID), pce.ContainerID)
	pce.FSGroup, _ = p.userGroupResolver.ResolveGroup(int(pce.Credentials.FSGID), pce.ContainerID)
}

// Get returns the cache entry for a specified pid
func (p *EBPFResolver) Get(pid uint32) *model.ProcessCacheEntry {
	p.RLock()
	defer p.RUnlock()
	return p.entryCache[pid]
}

// UpdateUID updates the credentials of the provided pid
func (p *EBPFResolver) UpdateUID(pid uint32, e *model.Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.UID = e.SetUID.UID
		entry.Credentials.User = e.FieldHandlers.ResolveSetuidUser(e, &e.SetUID)
		entry.Credentials.EUID = e.SetUID.EUID
		entry.Credentials.EUser = e.FieldHandlers.ResolveSetuidEUser(e, &e.SetUID)
		entry.Credentials.FSUID = e.SetUID.FSUID
		entry.Credentials.FSUser = e.FieldHandlers.ResolveSetuidFSUser(e, &e.SetUID)
	}
}

// UpdateGID updates the credentials of the provided pid
func (p *EBPFResolver) UpdateGID(pid uint32, e *model.Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.GID = e.SetGID.GID
		entry.Credentials.Group = e.FieldHandlers.ResolveSetgidGroup(e, &e.SetGID)
		entry.Credentials.EGID = e.SetGID.EGID
		entry.Credentials.EGroup = e.FieldHandlers.ResolveSetgidEGroup(e, &e.SetGID)
		entry.Credentials.FSGID = e.SetGID.FSGID
		entry.Credentials.FSGroup = e.FieldHandlers.ResolveSetgidFSGroup(e, &e.SetGID)
	}
}

// UpdateCapset updates the credentials of the provided pid
func (p *EBPFResolver) UpdateCapset(pid uint32, e *model.Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.CapEffective = e.Capset.CapEffective
		entry.Credentials.CapPermitted = e.Capset.CapPermitted
	}
}

// UpdateLoginUID updates the AUID of the provided pid
func (p *EBPFResolver) UpdateLoginUID(pid uint32, e *model.Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.AUID = e.LoginUIDWrite.AUID
	}
}

// UpdateAWSSecurityCredentials updates the list of AWS Security Credentials
func (p *EBPFResolver) UpdateAWSSecurityCredentials(pid uint32, e *model.Event) {
	if len(e.IMDS.AWS.SecurityCredentials.AccessKeyID) == 0 {
		return
	}

	p.Lock()
	defer p.Unlock()

	entry := p.entryCache[pid]
	if entry != nil {
		// check if this key is already in cache
		for _, key := range entry.AWSSecurityCredentials {
			if key.AccessKeyID == e.IMDS.AWS.SecurityCredentials.AccessKeyID {
				return
			}
		}
		entry.AWSSecurityCredentials = append(entry.AWSSecurityCredentials, e.IMDS.AWS.SecurityCredentials)
	}
}

// FetchAWSSecurityCredentials returns the list of AWS Security Credentials valid at the time of the event, and prunes
// expired entries
func (p *EBPFResolver) FetchAWSSecurityCredentials(e *model.Event) []model.AWSSecurityCredentials {
	p.Lock()
	defer p.Unlock()

	entry := p.entryCache[e.ProcessContext.Pid]
	if entry != nil {
		// check if we should delete
		var toDelete []int
		for id, key := range entry.AWSSecurityCredentials {
			if key.Expiration.Before(e.ResolveEventTime()) {
				toDelete = append([]int{id}, toDelete...)
			}
		}

		// delete expired entries
		for _, id := range toDelete {
			entry.AWSSecurityCredentials = append(entry.AWSSecurityCredentials[0:id], entry.AWSSecurityCredentials[id+1:]...)
		}

		return entry.AWSSecurityCredentials
	}
	return nil
}

// Start starts the resolver
func (p *EBPFResolver) Start(ctx context.Context) error {
	var err error
	if p.inodeFileMap, err = managerhelper.Map(p.manager, "inode_file"); err != nil {
		return err
	}

	if p.procCacheMap, err = managerhelper.Map(p.manager, "proc_cache"); err != nil {
		return err
	}

	if p.pidCacheMap, err = managerhelper.Map(p.manager, "pid_cache"); err != nil {
		return err
	}

	go p.cacheFlush(ctx)

	return nil
}

func (p *EBPFResolver) cacheFlush(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			procPids, err := process.Pids()
			if err != nil {
				continue
			}
			procPidsMap := make(map[uint32]bool)
			for _, pid := range procPids {
				procPidsMap[uint32(pid)] = true
			}

			p.Lock()
			for pid := range p.entryCache {
				if _, exists := procPidsMap[pid]; !exists {
					if entry := p.entryCache[pid]; entry != nil {
						p.exitedQueue = append(p.exitedQueue, pid)
					}
				}
			}
			p.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// SyncBoundSockets sets the bound sockets discovered during the snapshot
func (p *EBPFResolver) SyncBoundSockets(pid uint32, boundSockets []model.SnapshottedBoundSocket) {
	p.SnapshottedBoundSockets[pid] = boundSockets
}

// SyncCache snapshots /proc for the provided pid.
func (p *EBPFResolver) SyncCache(proc *process.Process) {
	// Only a R lock is necessary to check if the entry exists, but if it exists, we'll update it, so a RW lock is
	// required.
	p.Lock()
	defer p.Unlock()

	filledProc, err := utils.GetFilledProcess(proc)
	if err != nil {
		seclog.Tracef("unable to get a filled process for %d: %v", proc.Pid, err)
		return
	}

	if entry := p.newEntryFromProcfs(proc, filledProc, 0, model.ProcessCacheEntryFromSnapshot, nil); entry != nil {
		p.syncKernelMaps(entry)
	}
}

func (p *EBPFResolver) setAncestor(pce *model.ProcessCacheEntry) {
	parent := p.entryCache[pce.PPid]
	if parent != nil {
		pce.SetAncestor(parent)
	}
}

func (p *EBPFResolver) syncKernelMaps(entry *model.ProcessCacheEntry) {
	bootTime := p.timeResolver.GetBootTime()

	// insert new entry in kernel maps
	procCacheEntryB := make([]byte, 248)
	_, err := entry.Process.MarshalProcCache(procCacheEntryB, bootTime)
	if err != nil {
		seclog.Errorf("couldn't marshal proc_cache entry: %s", err)
	} else {
		if err = p.procCacheMap.Put(entry.Cookie, procCacheEntryB); err != nil {
			seclog.Errorf("couldn't push proc_cache entry to kernel space: %s", err)
		}
	}
	pidCacheEntryB := make([]byte, 88)
	_, err = entry.Process.MarshalPidCache(pidCacheEntryB, bootTime)
	if err != nil {
		seclog.Errorf("couldn't marshal pid_cache entry: %s", err)
	} else {
		if err = p.pidCacheMap.Put(entry.PIDContext.Pid, pidCacheEntryB); err != nil {
			seclog.Errorf("couldn't push pid_cache entry to kernel space: %s", err)
		}
	}
}

// newEntryFromProcfs creates a new process cache entry by snapshotting /proc for the provided pid
func (p *EBPFResolver) newEntryFromProcfs(proc *process.Process, filledProc *utils.FilledProcess, inode uint64, source uint64, newEntryCb func(*model.ProcessCacheEntry, error)) *model.ProcessCacheEntry {
	pid := uint32(proc.Pid)

	entry := p.NewProcessCacheEntry(model.PIDContext{Pid: pid, Tid: pid})

	// update the cache entry
	if err := p.enrichEventFromProcfs(entry, proc, filledProc); err != nil {
		seclog.Trace(err)
		return nil
	}

	// use the inode from the pid context if set so that we don't propagate a potentially wrong inode
	// it may happen if the activity is from a process (same pid) that was replaced since then.
	if inode != 0 {
		if entry.FileEvent.Inode != inode {
			seclog.Warnf("inode mismatch, using inode from pid context %d: %d != %d", pid, entry.FileEvent.Inode, inode)

			entry.FileEvent.Inode = inode
			entry.IsParentMissing = true
		}
	}

	entry.IsKworker = filledProc.Ppid == 0 && filledProc.Pid != 1

	parent := p.entryCache[entry.PPid]
	if parent != nil {
		if parent.Equals(entry) {
			entry.SetForkParent(parent)
		} else if prev := p.entryCache[pid]; prev != nil { // exec-exec
			entry.SetExecParent(prev)
		} else { // exec
			entry.SetExecParent(parent)
		}
	} else if pid == 1 {
		entry.SetAsExec()
	} else {
		seclog.Debugf("unable to set the type of process, not pid 1, no parent in cache: %+v", entry)
	}

	p.insertEntry(entry, source)

	seclog.Tracef("New process cache entry added: %s %s %d/%d", entry.Comm, entry.FileEvent.PathnameStr, pid, entry.FileEvent.Inode)

	if newEntryCb != nil {
		newEntryCb(entry, nil)
	}

	return entry
}

// ToJSON return a json version of the cache
func (p *EBPFResolver) ToJSON(raw bool) ([]byte, error) {
	dump := struct {
		Entries []json.RawMessage
	}{}

	p.Walk(func(entry *model.ProcessCacheEntry) {
		var (
			d   []byte
			err error
		)

		if raw {
			d, err = json.Marshal(entry)
		} else {
			e := struct {
				PID             uint32
				PPID            uint32
				Path            string
				Inode           uint64
				MountID         uint32
				Source          string
				ExecInode       uint64
				IsExec          bool
				IsParentMissing bool
				CGroup          string
				ContainerID     string
			}{
				PID:             entry.Pid,
				PPID:            entry.PPid,
				Path:            entry.FileEvent.PathnameStr,
				Inode:           entry.FileEvent.Inode,
				MountID:         entry.FileEvent.MountID,
				Source:          model.ProcessSourceToString(entry.Source),
				ExecInode:       entry.ExecInode,
				IsExec:          entry.IsExec,
				IsParentMissing: entry.IsParentMissing,
				CGroup:          string(entry.CGroup.CGroupID),
				ContainerID:     string(entry.ContainerID),
			}

			d, err = json.Marshal(e)
		}

		if err == nil {
			dump.Entries = append(dump.Entries, d)
		}
	})

	return json.Marshal(dump)
}

func (p *EBPFResolver) toDot(writer io.Writer, entry *model.ProcessCacheEntry, already map[string]bool, withArgs bool) {
	for entry != nil {
		label := fmt.Sprintf("%s:%d", entry.Comm, entry.Pid)
		if _, exists := already[label]; !exists {
			if !entry.ExitTime.IsZero() {
				label = "[" + label + "]"
			}

			if withArgs {
				argv, _ := p.GetProcessArgvScrubbed(&entry.Process)
				fmt.Fprintf(writer, `"%d:%s" [label="%s", comment="%s"];`, entry.Pid, entry.Comm, label, strings.Join(argv, " "))
			} else {
				fmt.Fprintf(writer, `"%d:%s" [label="%s"];`, entry.Pid, entry.Comm, label)
			}
			fmt.Fprintln(writer)

			already[label] = true
		}

		if entry.Ancestor != nil {
			relation := fmt.Sprintf(`"%d:%s" -> "%d:%s";`, entry.Ancestor.Pid, entry.Ancestor.Comm, entry.Pid, entry.Comm)
			if _, exists := already[relation]; !exists {
				fmt.Fprintln(writer, relation)

				already[relation] = true
			}
		}

		entry = entry.Ancestor
	}
}

// ToDot create a temp file and dump the cache
func (p *EBPFResolver) ToDot(withArgs bool) (string, error) {
	dump, err := os.CreateTemp("/tmp", "process-cache-dump-")
	if err != nil {
		return "", err
	}

	defer dump.Close()

	if err := os.Chmod(dump.Name(), 0400); err != nil {
		return "", err
	}

	p.RLock()
	defer p.RUnlock()

	fmt.Fprintf(dump, "digraph ProcessTree {\n")

	already := make(map[string]bool)
	for _, entry := range p.entryCache {
		p.toDot(dump, entry, already, withArgs)
	}

	fmt.Fprintf(dump, `}`)

	if err = dump.Close(); err != nil {
		return "", fmt.Errorf("could not close file [%s]: %w", dump.Name(), err)
	}
	return dump.Name(), nil
}

// getEntryCacheSize returns the cache size of the process resolver
func (p *EBPFResolver) getEntryCacheSize() float64 {
	p.RLock()
	defer p.RUnlock()
	return float64(len(p.entryCache))
}

// getProcessCacheEntryCount returns the cache size of the process resolver
func (p *EBPFResolver) getProcessCacheEntryCount() float64 {
	return float64(p.processCacheEntryCount.Load())
}

// SetState sets the process resolver state
func (p *EBPFResolver) SetState(state int64) {
	p.state.Store(state)
}

// Walk iterates through the entire tree and call the provided callback on each entry
func (p *EBPFResolver) Walk(callback func(entry *model.ProcessCacheEntry)) {
	p.RLock()
	defer p.RUnlock()

	for _, entry := range p.entryCache {
		callback(entry)
	}
}

// UpdateProcessCGroupContext updates the cgroup context and container ID of the process matching the provided PID
func (p *EBPFResolver) UpdateProcessCGroupContext(pid uint32, cgroupContext *model.CGroupContext, newEntryCb func(entry *model.ProcessCacheEntry, err error)) bool {
	p.Lock()
	defer p.Unlock()

	pce := p.resolve(pid, pid, 0, false, newEntryCb)
	if pce == nil {
		return false
	}

	// Assume that the container runtime from the kernel side may be incorrect or missing
	// In that case fallback to the userland container runtime.
	if !cgroupContext.CGroupFlags.IsSystemd() && cgroupContext.CGroupID != "" {
		pce.Process.ContainerID, cgroupContext.CGroupFlags = containerutils.FindContainerID(cgroupContext.CGroupID)
		pce.ContainerID = pce.Process.ContainerID
	}

	pce.Process.CGroup = *cgroupContext
	pce.CGroup = *cgroupContext

	return true
}

// NewEBPFResolver returns a new process resolver
func NewEBPFResolver(manager *manager.Manager, config *config.Config, statsdClient statsd.ClientInterface,
	scrubber *procutil.DataScrubber, containerResolver *container.Resolver, mountResolver mount.ResolverInterface,
	cgroupResolver *cgroup.Resolver, userGroupResolver *usergroup.Resolver, timeResolver *stime.Resolver,
	pathResolver spath.ResolverInterface, envVarsResolver *envvars.Resolver, opts *ResolverOpts) (*EBPFResolver, error) {
	argsEnvsCache, err := simplelru.NewLRU[uint64, *argsEnvsCacheEntry](maxParallelArgsEnvs, nil)
	if err != nil {
		return nil, err
	}

	p := &EBPFResolver{
		manager:                   manager,
		config:                    config,
		statsdClient:              statsdClient,
		scrubber:                  scrubber,
		entryCache:                make(map[uint32]*model.ProcessCacheEntry),
		SnapshottedBoundSockets:   make(map[uint32][]model.SnapshottedBoundSocket),
		opts:                      *opts,
		argsEnvsCache:             argsEnvsCache,
		state:                     atomic.NewInt64(Snapshotting),
		hitsStats:                 map[string]*atomic.Int64{},
		processCacheEntryCount:    atomic.NewInt64(0),
		missStats:                 atomic.NewInt64(0),
		addedEntriesFromEvent:     atomic.NewInt64(0),
		addedEntriesFromKernelMap: atomic.NewInt64(0),
		addedEntriesFromProcFS:    atomic.NewInt64(0),
		flushedEntries:            atomic.NewInt64(0),
		pathErrStats:              atomic.NewInt64(0),
		argsTruncated:             atomic.NewInt64(0),
		argsSize:                  atomic.NewInt64(0),
		envsTruncated:             atomic.NewInt64(0),
		envsSize:                  atomic.NewInt64(0),
		brokenLineage:             atomic.NewInt64(0),
		inodeErrStats:             atomic.NewInt64(0),
		containerResolver:         containerResolver,
		mountResolver:             mountResolver,
		cgroupResolver:            cgroupResolver,
		userGroupResolver:         userGroupResolver,
		timeResolver:              timeResolver,
		pathResolver:              pathResolver,
		envVarsResolver:           envVarsResolver,
	}
	for _, t := range metrics.AllTypesTags {
		p.hitsStats[t] = atomic.NewInt64(0)
	}
	p.processCacheEntryPool = NewProcessCacheEntryPool(func() { p.processCacheEntryCount.Dec() })

	// Create rate limiter that allows for 128 pids
	limiter, err := utils.NewLimiter[uint32](128, numAllowedPIDsToResolvePerPeriod, procFallbackLimiterPeriod)
	if err != nil {
		return nil, err
	}
	p.procFallbackLimiter = limiter

	return p, nil
}
