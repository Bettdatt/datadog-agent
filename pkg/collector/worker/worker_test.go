// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package worker

import (
	"expvar"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameimpl"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	haagentimpl "github.com/DataDog/datadog-agent/comp/haagent/impl"
	haagentmock "github.com/DataDog/datadog-agent/comp/haagent/mock"
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/aggregator/mocksender"
	"github.com/DataDog/datadog-agent/pkg/aggregator/sender"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	checkid "github.com/DataDog/datadog-agent/pkg/collector/check/id"
	"github.com/DataDog/datadog-agent/pkg/collector/check/stub"
	"github.com/DataDog/datadog-agent/pkg/collector/runner/expvars"
	"github.com/DataDog/datadog-agent/pkg/collector/runner/tracker"
	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/metrics/servicecheck"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
)

type testCheck struct {
	stub.StubCheck
	sync.Mutex
	doErr       bool
	doWarn      bool
	id          string
	longRunning bool
	t           *testing.T
	runFunc     func(id checkid.ID)
	runCount    *atomic.Uint64
}

func (c *testCheck) ID() checkid.ID { return checkid.ID(c.id) }
func (c *testCheck) String() string { return checkid.IDToCheckName(c.ID()) }
func (c *testCheck) RunCount() int  { return int(c.runCount.Load()) }

func (c *testCheck) Interval() time.Duration {
	if c.longRunning {
		return 0
	}

	return 123
}

func (c *testCheck) GetWarnings() []error {
	if c.doWarn {
		return []error{fmt.Errorf("Warning")}
	}

	return []error{}
}

func (c *testCheck) Run() error {
	if c.runFunc != nil {
		c.runFunc(c.ID())
	}

	c.runCount.Inc()

	c.Lock()
	defer c.Unlock()

	if c.doErr {
		return fmt.Errorf("myerror")
	}

	return nil
}

// Helpers

// AssertAsyncWorkerCount returns the expvar count of the currently-running
// workers. The function is exported since other tests in this directory use
// it as well.
func AssertAsyncWorkerCount(t *testing.T, count int) {
	for idx := 0; idx < 100; idx++ {
		workers := expvars.GetWorkerCount()
		if workers == count {
			// This may seem superfluous but we want to ensure that at least one
			// assertion runs in all cases
			require.Equal(t, count, workers)
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, count, expvars.GetWorkerCount())
}

func newCheck(t *testing.T, id string, doErr bool, runFunc func(checkid.ID)) *testCheck {
	return &testCheck{
		doErr:    doErr,
		t:        t,
		id:       id,
		runFunc:  runFunc,
		runCount: atomic.NewUint64(0),
	}
}

type testHACheck struct {
	testCheck
}

func (c *testHACheck) IsHASupported() bool {
	return true
}

func newHACheck(t *testing.T, id string, doErr bool, runFunc func(checkid.ID)) *testHACheck {
	return &testHACheck{
		testCheck: testCheck{
			doErr:    doErr,
			t:        t,
			id:       id,
			runFunc:  runFunc,
			runCount: atomic.NewUint64(0),
		},
	}
}

func assertErrorCount(t *testing.T, c check.Check, count int) {
	stats, found := expvars.CheckStats(c.ID())
	require.True(t, found)
	assert.Equal(t, count, int(stats.TotalErrors))
}

// Tests

func TestWorkerInit(t *testing.T) {
	checksTracker := &tracker.RunningChecksTracker{}
	pendingChecksChan := make(chan check.Check, 1)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	senderManager := aggregator.NewNoOpSenderManager()
	_, err := NewWorker(senderManager, haagentmock.NewMockHaAgent(), 1, 2, nil, checksTracker, mockShouldAddStatsFunc)
	require.NotNil(t, err)

	_, err = NewWorker(senderManager, haagentmock.NewMockHaAgent(), 1, 2, pendingChecksChan, nil, mockShouldAddStatsFunc)
	require.NotNil(t, err)

	_, err = NewWorker(senderManager, haagentmock.NewMockHaAgent(), 1, 2, pendingChecksChan, checksTracker, nil)
	require.NotNil(t, err)

	worker, err := NewWorker(senderManager, haagentmock.NewMockHaAgent(), 1, 2, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
	assert.Nil(t, err)
	assert.NotNil(t, worker)
}

func TestWorkerInitExpvarStats(t *testing.T) {
	checksTracker := &tracker.RunningChecksTracker{}
	pendingChecksChan := make(chan check.Check, 1)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	var wg sync.WaitGroup

	assert.Equal(t, 0, expvars.GetWorkerCount())

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 1, idx, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
			assert.Nil(t, err)

			worker.Run()
		}(i)
	}

	AssertAsyncWorkerCount(t, 20)

	close(pendingChecksChan)
	wg.Wait()

	AssertAsyncWorkerCount(t, 0)
}

func TestWorkerName(t *testing.T) {
	checksTracker := &tracker.RunningChecksTracker{}
	pendingChecksChan := make(chan check.Check, 1)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	for _, id := range []int{1, 100, 500} {
		expectedName := fmt.Sprintf("worker_%d", id)
		worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 1, id, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
		assert.Nil(t, err)
		assert.NotNil(t, worker)

		require.Equal(t, worker.Name, expectedName)
	}
}

func TestWorker(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	var wg sync.WaitGroup

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	testCheck1 := newCheck(t, "testing:123", false, nil)
	testCheck2 := newCheck(t, "testing2:234", false, nil)

	upperTimeStatLimit := time.Now().Add(-1 * time.Second)

	// This closure ensures that the mid-run worker state is correct too
	observerAssertFunc := func(id checkid.ID) {
		assert.Equal(t, 2, testCheck1.RunCount())
		assert.Equal(t, 1, testCheck2.RunCount())

		assert.Equal(t, 2, len(expvars.GetCheckStats()))
		_, found := expvars.CheckStats(id)
		assert.False(t, found)

		assert.Equal(t, 1, expvars.GetWorkerCount())

		assert.Equal(t, 1, int(expvars.GetRunningCheckCount()))
		assert.Equal(t, 1, len(checksTracker.RunningChecks()))
		assert.NotNil(t, checksTracker.RunningChecks()[id])

		assert.False(t, expvars.GetRunningStats(id).IsZero())
		assert.True(t, expvars.GetRunningStats(id).After(upperTimeStatLimit))
		assert.True(t, expvars.GetRunningStats(id).Before(time.Now().Add(1*time.Second)))
	}
	observerTestCheck := newCheck(t, "observer:123", false, observerAssertFunc)

	pendingChecksChan <- testCheck1
	pendingChecksChan <- testCheck2
	pendingChecksChan <- testCheck1
	pendingChecksChan <- observerTestCheck
	pendingChecksChan <- testCheck2
	pendingChecksChan <- testCheck1
	close(pendingChecksChan)

	worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 100, 200, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
	require.Nil(t, err)

	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.Run()
	}()

	wg.Wait()

	assert.Equal(t, 3, testCheck1.RunCount())
	assert.Equal(t, 2, testCheck2.RunCount())
	assert.Equal(t, 1, observerTestCheck.RunCount())

	assert.Equal(t, 3, len(expvars.GetCheckStats()))
	for _, expectedCheck := range []check.Check{
		testCheck1,
		testCheck2,
		observerTestCheck,
	} {
		_, found := expvars.CheckStats(expectedCheck.ID())
		assert.True(t, found)

		assert.True(t, expvars.GetRunningStats(expectedCheck.ID()).IsZero())
	}

	assert.Equal(t, 0, int(expvars.GetRunningCheckCount()))
	assert.Equal(t, 0, len(checksTracker.RunningChecks()))
	AssertAsyncWorkerCount(t, 0)
}

func TestWorkerUtilizationExpvars(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	var wg sync.WaitGroup

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	blockingCheck := newCheck(t, "testing:123", false, nil)
	longRunningCheck := &testCheck{
		t:           t,
		id:          "mycheck",
		longRunning: true,
		runCount:    atomic.NewUint64(0),
	}

	blockingCheck.Lock()
	longRunningCheck.Lock()

	worker, err := newWorkerWithOptions(
		1,
		2,
		pendingChecksChan,
		checksTracker,
		mockShouldAddStatsFunc,
		func() (sender.Sender, error) { return nil, nil },
		haagentmock.NewMockHaAgent(),
		100*time.Millisecond,
	)
	require.Nil(t, err)

	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.Run()
	}()

	// Clean things up
	defer func() {
		close(pendingChecksChan)
		wg.Wait()

		AssertAsyncWorkerCount(t, 0)
	}()

	// No tasks should equal no utilization
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.InDelta(c, getWorkerUtilizationExpvar(c, "worker_2"), 0, 0)
	}, 500*time.Millisecond, 100*time.Millisecond)

	// High util checks should be reflected in expvars

	pendingChecksChan <- blockingCheck

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.InDelta(c, getWorkerUtilizationExpvar(c, "worker_2"), 1, 0.05)
	}, 2*time.Second, 200*time.Millisecond)

	blockingCheck.Unlock()

	// Long running checks should also be counted as high utilization

	pendingChecksChan <- longRunningCheck

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.InDelta(c, getWorkerUtilizationExpvar(c, "worker_2"), 1, 0.05)
	}, 2*time.Second, 200*time.Millisecond)

	longRunningCheck.Unlock()
}

func TestWorkerErrorAndWarningHandling(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	var wg sync.WaitGroup

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }
	mockConfig = configmock.New(t)

	mockConfig.SetWithoutSource("hostname", "myhost")

	testCheck1 := newCheck(t, "testing:123", true, nil)
	testCheck2 := newCheck(t, "testing2:234", true, nil)
	testCheck3 := newCheck(t, "testing3:345", false, nil)

	for _, c := range []check.Check{
		testCheck1,
		testCheck2,
		testCheck3,
		testCheck3,
		testCheck1,
		testCheck1,
	} {
		pendingChecksChan <- c
	}
	close(pendingChecksChan)

	worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 100, 200, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
	require.Nil(t, err)
	AssertAsyncWorkerCount(t, 0)

	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.Run()
	}()

	wg.Wait()

	assert.Equal(t, 3, testCheck1.RunCount())
	assert.Equal(t, 1, testCheck2.RunCount())
	assert.Equal(t, 2, testCheck3.RunCount())

	assertErrorCount(t, testCheck1, 3)
	assertErrorCount(t, testCheck2, 1)
	assertErrorCount(t, testCheck3, 0)

	assert.Equal(t, 6, int(expvars.GetRunsCount()))
	assert.Equal(t, 4, int(expvars.GetErrorsCount()))
	assert.Equal(t, 0, int(expvars.GetWarningsCount()))

	AssertAsyncWorkerCount(t, 0)
}

func TestWorkerConcurrentCheckScheduling(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }
	mockConfig = configmock.New(t)

	mockConfig.SetWithoutSource("hostname", "myhost")

	testCheck := newCheck(t, "testing:123", true, nil)

	// Make it appear as though the check is already running
	checksTracker.AddCheck(testCheck)

	pendingChecksChan <- testCheck
	close(pendingChecksChan)

	worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 100, 200, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
	require.Nil(t, err)

	worker.Run()

	assert.Equal(t, 0, testCheck.RunCount())
	assert.Equal(t, 0, int(expvars.GetRunsCount()))
	assert.Equal(t, 0, int(expvars.GetErrorsCount()))
	assert.Equal(t, 0, int(expvars.GetWarningsCount()))
}

func TestWorkerStatsAddition(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)

	shouldAddStatsFunc := func(id checkid.ID) bool {
		return string(id) != "squelched:123"
	}
	mockConfig = configmock.New(t)

	mockConfig.SetWithoutSource("hostname", "myhost")

	longRunningCheckNoErrorNoWarning := &testCheck{
		t:           t,
		id:          "mycheck_noerr_nowarn",
		longRunning: true,
		runCount:    atomic.NewUint64(0),
	}

	longRunningCheckWithError := &testCheck{
		t:           t,
		id:          "mycheck_witherr",
		longRunning: true,
		doErr:       true,
		runCount:    atomic.NewUint64(0),
	}

	longRunningCheckWithWarnings := &testCheck{
		t:           t,
		id:          "mycheck_withwarn",
		longRunning: true,
		doWarn:      true,
		runCount:    atomic.NewUint64(0),
	}
	squelchedStatsCheck := newCheck(t, "squelched:123", false, nil)

	pendingChecksChan <- longRunningCheckNoErrorNoWarning
	pendingChecksChan <- longRunningCheckWithError
	pendingChecksChan <- longRunningCheckWithWarnings
	pendingChecksChan <- squelchedStatsCheck
	close(pendingChecksChan)

	worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentmock.NewMockHaAgent(), 100, 200, pendingChecksChan, checksTracker, shouldAddStatsFunc)
	require.Nil(t, err)

	worker.Run()

	for c, statsExpected := range map[check.Check]bool{
		longRunningCheckNoErrorNoWarning: false,
		longRunningCheckWithError:        true,
		longRunningCheckWithWarnings:     true,
		squelchedStatsCheck:              false,
	} {
		_, found := expvars.CheckStats(c.ID())
		assert.True(t, found == statsExpected)
	}
}

func TestWorkerServiceCheckSending(t *testing.T) {
	expvars.Reset()
	mockConfig := configmock.New(t)
	mockConfig.SetWithoutSource("hostname", "myhost")
	mockConfig.SetWithoutSource("integration_check_status_enabled", "true")

	var wg sync.WaitGroup

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	goodCheck := newCheck(t, "goodcheck:123", false, nil)
	checkWithError := newCheck(t, "check_witherr:123", true, nil)
	checkWithWarnings := &testCheck{
		t:        t,
		id:       "check_withwarn:123",
		doWarn:   true,
		runCount: atomic.NewUint64(0),
	}

	pendingChecksChan <- goodCheck
	pendingChecksChan <- checkWithWarnings
	pendingChecksChan <- checkWithError
	close(pendingChecksChan)

	mockSender := mocksender.NewMockSender("")

	worker, err := newWorkerWithOptions(
		100,
		200,
		pendingChecksChan,
		checksTracker,
		mockShouldAddStatsFunc,
		func() (sender.Sender, error) {
			return mockSender, nil
		},
		haagentmock.NewMockHaAgent(),
		pollingInterval,
	)
	require.Nil(t, err)

	mockSender.On("Commit").Return().Times(3)
	mockSender.On(
		"ServiceCheck",
		serviceCheckStatusKey,
		servicecheck.ServiceCheckOK,
		"myhost",
		[]string{"check:goodcheck", "dd_enable_check_intake:true"},
		"",
	).Return().Times(1)

	mockSender.On(
		"ServiceCheck",
		serviceCheckStatusKey,
		servicecheck.ServiceCheckWarning,
		"myhost",
		[]string{"check:check_withwarn", "dd_enable_check_intake:true"},
		"",
	).Return().Times(1)

	mockSender.On(
		"ServiceCheck",
		serviceCheckStatusKey,
		servicecheck.ServiceCheckCritical,
		"myhost",
		[]string{"check:check_witherr", "dd_enable_check_intake:true"},
		"",
	).Return().Times(1)

	// Run the worker
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.Run()
	}()

	wg.Wait()

	// Quick sanity check
	assert.Equal(t, 3, int(expvars.GetRunsCount()))

	// Go through the expectations
	mockSender.AssertExpectations(t)
	mockSender.AssertNumberOfCalls(t, "Commit", 3)
	mockSender.AssertNumberOfCalls(t, "ServiceCheck", 3)
}

func TestWorkerSenderNil(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	pendingChecksChan <- newCheck(t, "goodcheck:123", false, nil)
	close(pendingChecksChan)

	worker, err := newWorkerWithOptions(
		100,
		200,
		pendingChecksChan,
		checksTracker,
		mockShouldAddStatsFunc,
		func() (sender.Sender, error) {
			return nil, fmt.Errorf("testerr")
		},
		haagentmock.NewMockHaAgent(),
		pollingInterval,
	)
	require.Nil(t, err)

	// Implicit assertion that we don't panic
	worker.Run()

	// Quick sanity check
	assert.Equal(t, 1, int(expvars.GetRunsCount()))
}

func TestWorkerServiceCheckSendingLongRunningTasks(t *testing.T) {
	mockConfig := configmock.New(t)
	expvars.Reset()
	mockConfig.SetWithoutSource("hostname", "myhost")

	checksTracker := tracker.NewRunningChecksTracker()
	pendingChecksChan := make(chan check.Check, 10)
	mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

	longRunningCheck := &testCheck{
		t:           t,
		id:          "mycheck",
		longRunning: true,
		runCount:    atomic.NewUint64(0),
	}

	pendingChecksChan <- longRunningCheck
	close(pendingChecksChan)

	mockSender := mocksender.NewMockSender("")

	worker, err := newWorkerWithOptions(
		100,
		200,
		pendingChecksChan,
		checksTracker,
		mockShouldAddStatsFunc,
		func() (sender.Sender, error) {
			return mockSender, nil
		},
		haagentmock.NewMockHaAgent(),
		pollingInterval,
	)
	require.Nil(t, err)

	worker.Run()

	// Quick sanity check
	assert.Equal(t, 1, int(expvars.GetRunsCount()))

	mockSender.AssertNumberOfCalls(t, "Commit", 0)
	mockSender.AssertNumberOfCalls(t, "ServiceCheck", 0)
}

func TestWorker_HaIntegration(t *testing.T) {
	testHostname := "myhost"

	tests := []struct {
		name                         string
		haAgentEnabled               bool
		setLeaderValue               string
		expectedSnmpCheckRunCount    int
		expectedUnknownCheckRunCount int
	}{
		{
			name: "ha-agent enabled and is leader",
			// should run HA-integrations
			// should run "non HA integrations"
			haAgentEnabled:               true,
			setLeaderValue:               testHostname,
			expectedSnmpCheckRunCount:    1,
			expectedUnknownCheckRunCount: 1,
		},
		{
			name: "ha-agent enabled and not leader",
			// should skip HA-integrations
			// should run "non HA integrations"
			haAgentEnabled:               true,
			setLeaderValue:               "leader-is-another-agent",
			expectedSnmpCheckRunCount:    0,
			expectedUnknownCheckRunCount: 1,
		},
		{
			name: "ha-agent disabled",
			// When ha-agent is disabled, the agent behave as standalone agent (non HA) and will always run all integrations.
			// should run all integrations
			haAgentEnabled:               false,
			setLeaderValue:               "",
			expectedSnmpCheckRunCount:    1,
			expectedUnknownCheckRunCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expvars.Reset()

			var wg sync.WaitGroup

			checksTracker := tracker.NewRunningChecksTracker()
			pendingChecksChan := make(chan check.Check, 10)
			mockShouldAddStatsFunc := func(checkid.ID) bool { return true }

			snmpCheck := newHACheck(t, "snmp:123", false, nil)
			unknownCheck := newCheck(t, "unknown-check:123", false, nil)

			pendingChecksChan <- snmpCheck
			pendingChecksChan <- unknownCheck
			close(pendingChecksChan)

			agentConfigs := map[string]interface{}{
				"hostname":         testHostname,
				"ha_agent.enabled": tt.haAgentEnabled,
				"config_id":        "my-config-01",
			}
			logComponent := logmock.New(t)
			agentConfigComponent := fxutil.Test[config.Component](t, fx.Options(
				config.MockModule(),
				fx.Replace(config.MockParams{Overrides: agentConfigs}),
			))
			requires := haagentimpl.Requires{
				Logger:      logComponent,
				AgentConfig: agentConfigComponent,
				Hostname:    hostnameimpl.NewHostnameService(),
			}
			haagentcomp, _ := haagentimpl.NewComponent(requires)
			haagentcomp.Comp.SetLeader(tt.setLeaderValue)

			worker, err := NewWorker(aggregator.NewNoOpSenderManager(), haagentcomp.Comp, 100, 200, pendingChecksChan, checksTracker, mockShouldAddStatsFunc)
			require.Nil(t, err)

			wg.Add(1)
			go func() {
				defer wg.Done()
				worker.Run()
			}()

			wg.Wait()

			assert.Equal(t, tt.expectedSnmpCheckRunCount, snmpCheck.RunCount())
			assert.Equal(t, tt.expectedUnknownCheckRunCount, unknownCheck.RunCount())

			// make sure the check is deleted from checksTracker
			assert.Equal(t, 0, len(checksTracker.RunningChecks()))
		})
	}
}

// getWorkerUtilizationExpvar returns the utilization as presented by expvars
// for a named worker.
func getWorkerUtilizationExpvar(c *assert.CollectT, name string) float64 {
	runnerMapExpvar := expvar.Get("runner")
	require.NotNil(c, runnerMapExpvar)

	workersExpvar := runnerMapExpvar.(*expvar.Map).Get("Workers")
	require.NotNil(c, workersExpvar)

	instancesExpvar := workersExpvar.(*expvar.Map).Get("Instances")
	require.NotNil(c, instancesExpvar)

	workerStatsExpvar := instancesExpvar.(*expvar.Map).Get(name)
	require.NotNil(c, workerStatsExpvar)

	workerStats := workerStatsExpvar.(*expvars.WorkerStats)
	require.NotNil(c, workerStats)

	return workerStats.Utilization
}
