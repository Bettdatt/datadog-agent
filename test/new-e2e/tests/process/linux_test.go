// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package process

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DataDog/test-infra-definitions/components/datadog/agentparams"

	"github.com/DataDog/datadog-agent/test/fakeintake/aggregator"
	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/e2e"
	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/environments"
	awshost "github.com/DataDog/datadog-agent/test/new-e2e/pkg/provisioners/aws/host"
	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/utils/e2e/client/agentclient"
	"github.com/DataDog/datadog-agent/test/new-e2e/tests/agent-configuration/secretsutils"
)

type linuxTestSuite struct {
	e2e.BaseSuite[environments.Host]
}

func TestLinuxTestSuite(t *testing.T) {
	t.Parallel()
	agentParams := []func(*agentparams.Params) error{
		agentparams.WithAgentConfig(processCheckConfigStr),
	}

	options := []e2e.SuiteOption{
		e2e.WithProvisioner(awshost.Provisioner(awshost.WithAgentOptions(agentParams...))),
	}

	e2e.Run(t, &linuxTestSuite{}, options...)
}

func (s *linuxTestSuite) SetupSuite() {
	s.BaseSuite.SetupSuite()
	// SetupSuite needs to defer CleanupOnSetupFailure() if what comes after BaseSuite.SetupSuite() can fail.
	defer s.CleanupOnSetupFailure()

	// Start a process and keep it running
	s.Env().RemoteHost.MustExecute("sudo apt-get -y install stress")
	s.Env().RemoteHost.MustExecute("nohup stress -d 1 >myscript.log 2>&1 </dev/null &")
}

func (s *linuxTestSuite) TestAPIKeyRefresh() {
	t := s.T()

	secretClient := secretsutils.NewClient(t, s.Env().RemoteHost, "/tmp/test-secret")
	secretClient.SetSecret("api_key", "abcdefghijklmnopqrstuvwxyz123456")

	s.UpdateEnv(
		awshost.Provisioner(
			awshost.WithAgentOptions(
				agentparams.WithAgentConfig(processAgentRefreshStr),
				secretsutils.WithUnixSetupScript("/tmp/test-secret/secret-resolver.py", false),
				agentparams.WithSkipAPIKeyInConfig(),
			),
		),
	)

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, "abcdefghijklmnopqrstuvwxyz123456", s.Env().Agent.Client, false)
		assertLastPayloadAPIKey(collect, "abcdefghijklmnopqrstuvwxyz123456", s.Env().FakeIntake.Client())
	}, 2*time.Minute, 10*time.Second)

	// API key refresh
	secretClient.SetSecret("api_key", "123456abcdefghijklmnopqrstuvwxyz")
	secretRefreshOutput := s.Env().Agent.Client.Secret(agentclient.WithArgs([]string{"refresh"}))
	require.Contains(t, secretRefreshOutput, "api_key")

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, "123456abcdefghijklmnopqrstuvwxyz", s.Env().Agent.Client, false)
		assertLastPayloadAPIKey(collect, "123456abcdefghijklmnopqrstuvwxyz", s.Env().FakeIntake.Client())
	}, 2*time.Minute, 10*time.Second)
}

func (s *linuxTestSuite) TestAPIKeyRefreshCoreAgent() {
	t := s.T()

	secretClient := secretsutils.NewClient(t, s.Env().RemoteHost, "/tmp/test-secret")
	secretClient.SetSecret("api_key", "abcdefghijklmnopqrstuvwxyz123456")

	s.UpdateEnv(
		awshost.Provisioner(
			awshost.WithAgentOptions(
				agentparams.WithAgentConfig(coreAgentRefreshStr),
				secretsutils.WithUnixSetupScript("/tmp/test-secret/secret-resolver.py", false),
				agentparams.WithSkipAPIKeyInConfig(),
			),
		),
	)

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, "abcdefghijklmnopqrstuvwxyz123456", s.Env().Agent.Client, true)
		assertLastPayloadAPIKey(collect, "abcdefghijklmnopqrstuvwxyz123456", s.Env().FakeIntake.Client())
	}, 2*time.Minute, 10*time.Second)

	// API key refresh
	secretClient.SetSecret("api_key", "123456abcdefghijklmnopqrstuvwxyz")
	secretRefreshOutput := s.Env().Agent.Client.Secret(agentclient.WithArgs([]string{"refresh"}))
	require.Contains(t, secretRefreshOutput, "api_key")

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, "123456abcdefghijklmnopqrstuvwxyz", s.Env().Agent.Client, true)
		assertLastPayloadAPIKey(collect, "123456abcdefghijklmnopqrstuvwxyz", s.Env().FakeIntake.Client())
	}, 2*time.Minute, 10*time.Second)
}

func (s *linuxTestSuite) TestAPIKeyRefreshAdditionalEndpoints() {
	t := s.T()

	fakeIntakeURL := s.Env().FakeIntake.Client().URL()

	additionalEndpoint := fmt.Sprintf(`  additional_endpoints:
    "%s":
      - ENC[api_key_additional]`, fakeIntakeURL)
	config := coreAgentRefreshStr + additionalEndpoint

	secretClient := secretsutils.NewClient(t, s.Env().RemoteHost, "/tmp/test-secret")
	apiKey := "apikeyabcde"
	apiKeyAdditional := "apikey12345"
	secretClient.SetSecret("api_key", apiKey)
	secretClient.SetSecret("api_key_additional", apiKeyAdditional)

	s.UpdateEnv(
		awshost.Provisioner(
			awshost.WithAgentOptions(
				agentparams.WithAgentConfig(config),
				secretsutils.WithUnixSetupScript("/tmp/test-secret/secret-resolver.py", false),
				agentparams.WithSkipAPIKeyInConfig(),
			),
		),
	)

	fakeIntakeClient := s.Env().FakeIntake.Client()
	agentClient := s.Env().Agent.Client

	fakeIntakeClient.FlushServerAndResetAggregators()

	// Assert that the status and payloads have the correct API key
	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, apiKey, agentClient, true)
		assertAPIKeyStatus(collect, apiKeyAdditional, agentClient, true)
		assertAllPayloadsAPIKeys(collect, []string{apiKey, apiKeyAdditional}, fakeIntakeClient)
	}, 2*time.Minute, 10*time.Second)

	// Refresh secrets in the agent
	apiKey = "apikeyfghijk"
	apiKeyAdditional = "apikey67890"
	secretClient.SetSecret("api_key", apiKey)
	secretClient.SetSecret("api_key_additional", apiKeyAdditional)
	secretRefreshOutput := s.Env().Agent.Client.Secret(agentclient.WithArgs([]string{"refresh"}))
	require.Contains(t, secretRefreshOutput, "api_key")
	require.Contains(t, secretRefreshOutput, "api_key_additional")

	fakeIntakeClient.FlushServerAndResetAggregators()

	// Assert that the status and payloads have the correct API key
	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertAPIKeyStatus(collect, apiKey, agentClient, true)
		assertAPIKeyStatus(collect, apiKeyAdditional, agentClient, true)
		assertAllPayloadsAPIKeys(collect, []string{apiKey, apiKeyAdditional}, fakeIntakeClient)
	}, 2*time.Minute, 10*time.Second)
}

func (s *linuxTestSuite) TestProcessCheck() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{"process", "rtprocess"}, false)
	}, 2*time.Minute, 5*time.Second)

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, false, "stress")
}

func (s *linuxTestSuite) TestProcessDiscoveryCheck() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processDiscoveryCheckConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{"process_discovery"}, false)
	}, 1*time.Minute, 5*time.Second)

	var payloads []*aggregator.ProcessDiscoveryPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcessDiscoveries()
		assert.NoError(c, err, "failed to get process discovery payloads from fakeintake")
		assert.NotEmpty(c, payloads, "no process discovery payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessDiscoveryCollected(t, payloads, "stress")
}

func (s *linuxTestSuite) TestProcessCheckWithIO() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckConfigStr), agentparams.WithSystemProbeConfig(systemProbeConfigStr))))

	// Flush fake intake to remove payloads that won't have IO stats
	s.Env().FakeIntake.Client().FlushServerAndResetAggregators()

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{"process", "rtprocess"}, true)
	}, 1*time.Minute, 5*time.Second)

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, true, "stress")
}

func (s *linuxTestSuite) TestProcessChecksInCoreAgent() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckInCoreAgentConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{}, false)
	}, 1*time.Minute, 5*time.Second)

	// Verify that the process agent is not running
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status := s.Env().RemoteHost.MustExecute("sudo /opt/datadog-agent/embedded/bin/process-agent status")
		assert.Contains(c, status, "The Process Agent is not running")
	}, 1*time.Minute, 5*time.Second)

	// Flush fake intake to remove any payloads which may have
	s.Env().FakeIntake.Client().FlushServerAndResetAggregators()

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, false, "stress")

	// check that the process agent is not collected as it should not be running
	requireProcessNotCollected(t, payloads, "process-agent")
}

func (s *linuxTestSuite) TestProcessChecksWLM() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckInCoreAgentWLMProcessCollectorConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{}, false)
	}, 1*time.Minute, 5*time.Second)

	// Verify that the process agent is not running
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		status := s.Env().RemoteHost.MustExecute("sudo /opt/datadog-agent/embedded/bin/process-agent status")
		assert.Contains(c, status, "The Process Agent is not running")
	}, 1*time.Minute, 5*time.Second)

	// Flush fake intake to remove any payloads which may have
	s.Env().FakeIntake.Client().FlushServerAndResetAggregators()

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, false, "stress")

	// check that the process agent is not collected as it should not be running
	requireProcessNotCollected(t, payloads, "process-agent")
}

func (s *linuxTestSuite) TestProcessChecksInCoreAgentWithNPM() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckInCoreAgentConfigStr), agentparams.WithSystemProbeConfig(systemProbeNPMConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{"connections"}, false)
	}, 1*time.Minute, 5*time.Second)

	// Flush fake intake to remove any payloads which may have
	s.Env().FakeIntake.Client().FlushServerAndResetAggregators()

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, false, "stress")
}

func (s *linuxTestSuite) TestProcessChecksWithNPM() {
	t := s.T()
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(agentparams.WithAgentConfig(processCheckConfigStr), agentparams.WithSystemProbeConfig(systemProbeNPMConfigStr))))

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assertRunningChecks(collect, s.Env().Agent.Client, []string{"process", "rtprocess", "connections"}, false)
	}, 1*time.Minute, 5*time.Second)

	// Flush fake intake to remove any payloads which may have
	s.Env().FakeIntake.Client().FlushServerAndResetAggregators()

	var payloads []*aggregator.ProcessPayload
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		payloads, err = s.Env().FakeIntake.Client().GetProcesses()
		assert.NoError(c, err, "failed to get process payloads from fakeintake")

		// Wait for two payloads, as processes must be detected in two check runs to be returned
		assert.GreaterOrEqual(c, len(payloads), 2, "fewer than 2 payloads returned")
	}, 2*time.Minute, 10*time.Second)

	assertProcessCollected(t, payloads, false, "stress")
}

func (s *linuxTestSuite) TestManualProcessCheck() {
	check := s.Env().RemoteHost.MustExecute("sudo /opt/datadog-agent/embedded/bin/process-agent check process --json")

	assertManualProcessCheck(s.T(), check, false, "stress")
}

func (s *linuxTestSuite) TestManualProcessDiscoveryCheck() {
	check := s.Env().RemoteHost.MustExecute("sudo /opt/datadog-agent/embedded/bin/process-agent check process_discovery --json")

	assertManualProcessDiscoveryCheck(s.T(), check, "stress")
}

func (s *linuxTestSuite) TestManualProcessCheckWithIO() {
	s.UpdateEnv(awshost.Provisioner(awshost.WithAgentOptions(
		agentparams.WithAgentConfig(processCheckConfigStr),
		agentparams.WithSystemProbeConfig(systemProbeConfigStr))))

	check := s.Env().RemoteHost.MustExecute("sudo /opt/datadog-agent/embedded/bin/process-agent check process --json")

	assertManualProcessCheck(s.T(), check, true, "stress")
}
