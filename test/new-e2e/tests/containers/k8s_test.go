// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2023-present Datadog, Inc.

package containers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DataDog/agent-payload/v5/cyclonedx_v1_4"
	"github.com/DataDog/agent-payload/v5/sbom"
	"github.com/DataDog/test-infra-definitions/components/datadog/apps"
	"gopkg.in/zorkian/go-datadog-api.v2"

	"github.com/DataDog/datadog-agent/pkg/util/pointer"
	"github.com/DataDog/datadog-agent/test/fakeintake/aggregator"
	fakeintake "github.com/DataDog/datadog-agent/test/fakeintake/client"
	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/environments"

	"github.com/fatih/color"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
)

const (
	kubeNamespaceDogstatsWorkload           = "workload-dogstatsd"
	kubeNamespaceDogstatsStandaloneWorkload = "workload-dogstatsd-standalone"
	kubeNamespaceTracegenWorkload           = "workload-tracegen"
	kubeDeploymentDogstatsdUDP              = "dogstatsd-udp"
	kubeDeploymentDogstatsdUDPOrigin        = "dogstatsd-udp-origin-detection"
	kubeDeploymentDogstatsdUDPExternalData  = "dogstatsd-udp-external-data-only"
	kubeDeploymentDogstatsdUDSWithCSI       = "dogstatsd-uds-with-csi"
	kubeDeploymentDogstatsdUDS              = "dogstatsd-uds"
	kubeDeploymentTracegenTCPWorkload       = "tracegen-tcp"
	kubeDeploymentTracegenUDSWorkload       = "tracegen-uds"
)

var GitCommit string

type k8sSuite struct {
	baseSuite[environments.Kubernetes]
}

func (suite *k8sSuite) SetupSuite() {
	suite.baseSuite.SetupSuite()
	suite.clusterName = suite.Env().KubernetesCluster.ClusterName
}

func (suite *k8sSuite) TearDownSuite() {
	suite.baseSuite.TearDownSuite()

	color.NoColor = false
	c := color.New(color.Bold).SprintfFunc()
	suite.T().Log(c("The data produced and asserted by these tests can be viewed on this dashboard:"))
	c = color.New(color.Bold, color.FgBlue).SprintfFunc()
	suite.T().Log(c("https://dddev.datadoghq.com/dashboard/qcp-brm-ysc/e2e-tests-containers-k8s?refresh_mode=paused&tpl_var_kube_cluster_name%%5B0%%5D=%s&tpl_var_fake_intake_task_family%%5B0%%5D=%s-fakeintake-ecs&from_ts=%d&to_ts=%d&live=false",
		suite.clusterName,
		suite.clusterName,
		suite.StartTime().UnixMilli(),
		suite.EndTime().UnixMilli(),
	))
}

// Once pulumi has finished to create a stack, it can still take some time for the images to be pulled,
// for the containers to be started, for the agent collectors to collect workload information
// and to feed workload meta and the tagger.
//
// We could increase the timeout of all tests to cope with the agent tagger warmup time.
// But in case of a single bug making a single tag missing from every metric,
// all the tests would time out and that would be a waste of time.
//
// It’s better to have the first test having a long timeout to wait for the agent to warmup,
// and to have the following tests with a smaller timeout.
//
// Inside a testify test suite, tests are executed in alphabetical order.
// The 00 in Test00UpAndRunning is here to guarantee that this test, waiting for the agent pods to be ready,
// is run first.
func (suite *k8sSuite) Test00UpAndRunning() {
	timeout := 10 * time.Minute
	// Windows FIPS images are bigger and take longer to pull and start
	if suite.Env().Agent.FIPSEnabled {
		timeout = 20 * time.Minute
	}
	suite.testUpAndRunning(timeout)
}

// An agent restart (because of a health probe failure or because of a OOM kill for ex.)
// can cause a completely random failure on a completely random test.
// A metric can be fully missing if the agent is restarted when the metric is checked.
// Only a subset of tags can be missing if the agent has just restarted, but not all the
// collectors have finished to feed workload meta and the tagger.
// So, checking if any agent has restarted during the tests can be valuable for investigations.
//
// Inside a testify test suite, tests are executed in alphabetical order.
// The ZZ in TestZZUpAndRunning is here to guarantee that this test, is run last.
func (suite *k8sSuite) TestZZUpAndRunning() {
	suite.testUpAndRunning(1 * time.Minute)
}

func (suite *k8sSuite) testUpAndRunning(waitFor time.Duration) {
	ctx := context.Background()

	suite.Run("agent pods are ready and not restarting", func() {
		suite.EventuallyWithTf(func(c *assert.CollectT) {
			linuxNodes, err := suite.Env().KubernetesCluster.Client().CoreV1().Nodes().List(ctx, metav1.ListOptions{
				LabelSelector: fields.AndSelectors(
					fields.OneTermEqualSelector("kubernetes.io/os", "linux"),
					fields.OneTermNotEqualSelector("eks.amazonaws.com/compute-type", "fargate"),
				).String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list Linux nodes") {
				return
			}

			windowsNodes, err := suite.Env().KubernetesCluster.Client().CoreV1().Nodes().List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("kubernetes.io/os", "windows").String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list Windows nodes") {
				return
			}

			linuxPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxNodeAgent.LabelSelectors["app"]).String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list Linux datadog agent pods") {
				return
			}

			windowsPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.WindowsNodeAgent.LabelSelectors["app"]).String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list Windows datadog agent pods") {
				return
			}

			clusterAgentPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxClusterAgent.LabelSelectors["app"]).String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list datadog cluster agent pods") {
				return
			}

			clusterChecksPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxClusterChecks.LabelSelectors["app"]).String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list datadog cluster checks runner pods") {
				return
			}

			dogstatsdPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("dogstatsd-standalone").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", "dogstatsd-standalone").String(),
			})
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to list dogstatsd standalone pods") {
				return
			}

			assert.Len(c, linuxPods.Items, len(linuxNodes.Items))
			assert.Len(c, windowsPods.Items, len(windowsNodes.Items))
			assert.NotEmpty(c, clusterAgentPods.Items)
			assert.NotEmpty(c, clusterChecksPods.Items)
			assert.Len(c, dogstatsdPods.Items, len(linuxNodes.Items))

			for _, podList := range []*corev1.PodList{linuxPods, windowsPods, clusterAgentPods, clusterChecksPods, dogstatsdPods} {
				for _, pod := range podList.Items {
					for _, containerStatus := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
						assert.Truef(c, containerStatus.Ready, "Container %s of pod %s isn’t ready", containerStatus.Name, pod.Name)
						assert.Zerof(c, containerStatus.RestartCount, "Container %s of pod %s has restarted", containerStatus.Name, pod.Name)
					}
				}
			}
		}, waitFor, 10*time.Second, "Not all agents eventually became ready in time.")
	})
}

func (suite *k8sSuite) TestAdmissionControllerWebhooksExist() {
	ctx := context.Background()
	expectedWebhookName := "datadog-webhook"

	suite.Run("agent registered mutating webhook configuration", func() {
		mutatingConfig, err := suite.Env().KubernetesCluster.Client().AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, expectedWebhookName, metav1.GetOptions{})
		suite.Require().NoError(err)
		suite.NotNilf(mutatingConfig, "None of the mutating webhook configurations have the name '%s'", expectedWebhookName)
	})

	suite.Run("agent registered validating webhook configuration", func() {
		validatingConfig, err := suite.Env().KubernetesCluster.Client().AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, expectedWebhookName, metav1.GetOptions{})
		suite.Require().NoError(err)
		suite.NotNilf(validatingConfig, "None of the validating webhook configurations have the name '%s'", expectedWebhookName)
	})
}

func (suite *k8sSuite) TestVersion() {
	ctx := context.Background()
	versionExtractor := regexp.MustCompile(`Commit: ([[:xdigit:]]+)`)

	for _, tt := range []struct {
		podType     string
		appSelector string
		container   string
	}{
		{
			"Linux agent",
			suite.Env().Agent.LinuxNodeAgent.LabelSelectors["app"],
			"agent",
		},
		{
			"Windows agent",
			suite.Env().Agent.WindowsNodeAgent.LabelSelectors["app"],
			"agent",
		},
		{
			"cluster agent",
			suite.Env().Agent.LinuxClusterAgent.LabelSelectors["app"],
			"cluster-agent",
		},
		{
			"cluster checks",
			suite.Env().Agent.LinuxClusterChecks.LabelSelectors["app"],
			"agent",
		},
	} {
		suite.Run(tt.podType+" pods are running the good version", func() {
			linuxPods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", tt.appSelector).String(),
				Limit:         1,
			})
			if suite.NoError(err) && len(linuxPods.Items) >= 1 {
				stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", linuxPods.Items[0].Name, tt.container, []string{"agent", "version"})
				if suite.NoError(err) {
					suite.Emptyf(stderr, "Standard error of `agent version` should be empty,")
					match := versionExtractor.FindStringSubmatch(stdout)
					if suite.Equalf(2, len(match), "'Commit' not found in the output of `agent version`.") {
						if suite.Greaterf(len(GitCommit), 6, "Couldn’t guess the expected version of the agent.") &&
							suite.Greaterf(len(match[1]), 6, "Couldn’t find the version of the agent.") {

							size2compare := len(GitCommit)
							if len(match[1]) < size2compare {
								size2compare = len(match[1])
							}

							suite.Equalf(GitCommit[:size2compare], match[1][:size2compare], "Agent isn’t running the expected version")
						}
					}
				}
			}
		})
	}
}

func (suite *k8sSuite) TestCLI() {
	suite.Run("agent CLI", func() { suite.testAgentCLI() })
	suite.Run("cluster agent CLI", func() { suite.testClusterAgentCLI() })
}

func (suite *k8sSuite) testAgentCLI() {
	ctx := context.Background()

	pod, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
		LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxNodeAgent.LabelSelectors["app"]).String(),
		Limit:         1,
	})
	suite.Require().NoError(err)
	suite.Require().Len(pod.Items, 1)

	suite.Run("agent status", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"agent", "status"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `agent status` should be empty")
		suite.Contains(stdout, "Collector")
		suite.Contains(stdout, "Running Checks")
		suite.Contains(stdout, "Instance ID: container [OK]")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent status --json", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"env", "DD_LOG_LEVEL=off", "agent", "status", "--json"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `agent status` should be empty")
		if !suite.Truef(json.Valid([]byte(stdout)), "Output of `agent status --json` isn’t valid JSON") {
			var blob interface{}
			err := json.Unmarshal([]byte(stdout), &blob)
			suite.NoError(err)
		}
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent checkconfig", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"agent", "checkconfig"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `agent checkconfig` should be empty")
		suite.Contains(stdout, "=== container check ===")
		suite.Contains(stdout, "Config for instance ID: container:")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent check -r container", func() {
		var stdout string
		suite.EventuallyWithT(func(c *assert.CollectT) {
			stdout, _, err = suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"agent", "check", "-t", "3", "container", "--table", "--delay", "1000", "--pause", "5000"})
			// Can be replaced by require.NoError(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoError(c, err) {
				return
			}
			matched, err := regexp.MatchString(`container\.memory\.usage\s+gauge\s+\d+\s+\d+`, stdout)
			if assert.NoError(c, err) {
				assert.Truef(c, matched, "Output of `agent check -r container` doesn’t contain the expected metric")
			}
		}, 2*time.Minute, 1*time.Second)
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent check -r container --json", func() {
		var stdout string
		suite.EventuallyWithT(func(c *assert.CollectT) {
			stdout, _, err = suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"env", "DD_LOG_LEVEL=off", "agent", "check", "-r", "container", "--table", "--delay", "1000", "--json"})
			// Can be replaced by require.NoError(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoError(c, err) {
				return
			}
			if !assert.Truef(c, json.Valid([]byte(stdout)), "Output of `agent check -r container --json` isn’t valid JSON") {
				var blob interface{}
				err := json.Unmarshal([]byte(stdout), &blob)
				assert.NoError(c, err)
			}
		}, 2*time.Minute, 1*time.Second)
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent workload-list", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"agent", "workload-list", "-v"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `agent workload-list` should be empty")
		suite.Contains(stdout, "=== Entity container sources(merged):[node_orchestrator runtime] id: ")
		suite.Contains(stdout, "=== Entity kubernetes_pod sources(merged):[cluster_orchestrator node_orchestrator] id: ")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("agent tagger-list", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pod.Items[0].Name, "agent", []string{"agent", "tagger-list"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `agent tagger-list` should be empty")
		suite.Contains(stdout, "=== Entity container_id://")
		suite.Contains(stdout, "=== Entity kubernetes_pod_uid://")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})
}

func (suite *k8sSuite) testClusterAgentCLI() {
	leaderDcaPodName := suite.testDCALeaderElection(false) //check cluster agent leaderelection without restart
	suite.Require().NotEmpty(leaderDcaPodName, "Leader DCA pod name should not be empty")

	suite.Run("cluster-agent status", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", leaderDcaPodName, "cluster-agent", []string{"datadog-cluster-agent", "status"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `datadog-cluster-agent status` should be empty")
		suite.Contains(stdout, "Collector")
		suite.Contains(stdout, "Running Checks")
		suite.Contains(stdout, "kubernetes_state_core")
		if suite.Env().Agent.FIPSEnabled {
			// Verify FIPS mode is reported as enabled
			suite.Contains(stdout, "FIPS Mode: enabled", "Cluster agent should report FIPS Mode as enabled")
			suite.T().Logf("Cluster agent correctly reports FIPS status as enabled")
		} else {
			suite.NotContains(stdout, "FIPS Mode: enabled", "Cluster agent should not report FIPS Mode as enabled")
		}
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("cluster-agent version", func() {
		if suite.Env().Agent.FIPSEnabled {
			stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", leaderDcaPodName, "cluster-agent", []string{"datadog-cluster-agent", "version"})
			suite.Require().NoError(err)
			suite.Empty(stderr, "Standard error of `datadog-cluster-agent version` should be empty")
			suite.Contains(stdout, "Cluster Agent")
			// Verify cluster agent is built with Go-Boring SSL (X:boringcrypto)
			suite.Contains(stdout, "X:boringcrypto", "Cluster agent must be built with Go-Boring SSL")
			suite.T().Logf("Cluster agent correctly reports Go-Boring SSL via X:boringcrypto flag")
			if suite.T().Failed() {
				suite.T().Log(stdout)
			}
		}
	})

	suite.Run("cluster-agent status --json", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", leaderDcaPodName, "cluster-agent", []string{"env", "DD_LOG_LEVEL=off", "datadog-cluster-agent", "status", "--json"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `datadog-cluster-agent status` should be empty")
		if !suite.Truef(json.Valid([]byte(stdout)), "Output of `datadog-cluster-agent status --json` isn’t valid JSON") {
			var blob interface{}
			err := json.Unmarshal([]byte(stdout), &blob)
			suite.NoError(err)
		}
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("cluster-agent checkconfig", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", leaderDcaPodName, "cluster-agent", []string{"datadog-cluster-agent", "checkconfig"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `datadog-cluster-agent checkconfig` should be empty")
		suite.Contains(stdout, "=== kubernetes_state_core check ===")
		suite.Contains(stdout, "Config for instance ID: kubernetes_state_core:")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})

	suite.Run("cluster-agent clusterchecks", func() {
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", leaderDcaPodName, "cluster-agent", []string{"datadog-cluster-agent", "clusterchecks"})
		suite.Require().NoError(err)
		suite.Empty(stderr, "Standard error of `datadog-cluster-agent clusterchecks` should be empty")
		suite.Contains(stdout, "agents reporting ===")
		suite.Contains(stdout, "===== Checks on ")
		suite.Contains(stdout, "=== helm check ===")
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	})
}

func (suite *k8sSuite) testDCALeaderElection(restartLeader bool) string {
	ctx := context.Background()
	var leaderPodName string

	success := suite.EventuallyWithTf(func(c *assert.CollectT) {
		// find cluster agent pod, it could be either leader or follower
		pods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
			LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxClusterAgent.LabelSelectors["app"]).String(),
			Limit:         1,
		})
		assert.NoError(c, err)
		assert.Len(c, pods.Items, 1, "Expected at least one running cluster agent pod")
		stdout, stderr, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", pods.Items[0].Name, "cluster-agent", []string{"env", "DD_LOG_LEVEL=off", "datadog-cluster-agent", "status", "--json"})
		assert.NoError(c, err)
		assert.Empty(c, stderr, "Standard error of `datadog-cluster-agent status` should be empty")
		var blob interface{}
		err = json.Unmarshal([]byte(stdout), &blob)
		assert.NoError(c, err, "Failed to unmarshal JSON output of `datadog-cluster-agent status --json`")

		// Check if the "leaderelection" field exists
		assert.NotNil(c, blob, "Failed to unmarshal JSON output of `datadog-cluster-agent status --json`")
		blobMap, ok := blob.(map[string]interface{})
		assert.Truef(c, ok, "Failed to assert blob as map[string]interface{}")

		// Check if the "leaderName" field exists
		assert.Contains(c, blobMap, "leaderelection", "Field `leaderelection` not found in the JSON output")
		assert.Contains(c, blobMap["leaderelection"], "leaderName", "Field `leaderelection.leaderName` not found in the JSON output")
		leaderPodName, ok = (blobMap["leaderelection"].(map[string]interface{}))["leaderName"].(string)
		assert.Truef(c, ok, "Failed to assert `leaderelection.leaderName` as string")
		assert.NotEmpty(c, leaderPodName, "Field `leaderelection.leaderName` is empty in the JSON output")
		suite.T().Logf("Cluster agent pods have a leader name: %s", leaderPodName)
		leaderPod, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").Get(ctx, leaderPodName, metav1.GetOptions{})
		assert.NoError(c, err)
		assert.NotNilf(c, leaderPod, "Leader pod: %s not found", leaderPodName)

		// restart the leader pod
		if restartLeader {
			// TODO: not enabled for now because it will cause other tests to fail
			suite.T().Logf("Restarting leader pod: %s", leaderPodName)
			err = suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").Delete(ctx, leaderPodName, metav1.DeleteOptions{})
			assert.NoError(c, err)
		}
		if suite.T().Failed() {
			suite.T().Log(stdout)
		}
	}, 2*time.Minute, 10*time.Second, "Cluster agent leader election failed")

	if !success {
		return ""
	}
	return leaderPodName
}

func (suite *k8sSuite) TestNginx() {
	// `nginx` check is configured via AD annotation on pods
	// Test it is properly scheduled
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "nginx.net.request_per_s",
			Tags: []string{
				`^kube_namespace:workload-nginx$`,
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:nginx$`,
				`^display_container_name:nginx`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source docker image label
				`^image_id:ghcr\.io/datadog/apps-nginx-server@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-nginx-server$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:nginx$`,
				`^kube_deployment:nginx$`,
				`^kube_namespace:workload-nginx$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:nginx-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:nginx-[[:alnum:]]+$`,
				`^kube_service:nginx$`,
				`^pod_name:nginx-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:apps-nginx-server$`,
				`^domain:deployment$`,
				`^mail:team-container-platform@datadoghq.com$`,
				`^org:agent-org$`,
				`^parent-name:nginx$`,
				`^team:contp$`,
			},
			AcceptUnexpectedTags: true,
		},
	})

	// `http_check` is configured via AD annotation on service
	// Test it is properly scheduled
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "network.http.response_time",
			Tags: []string{
				`^kube_namespace:workload-nginx$`,
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^cluster_name:`,
				`^instance:My_Nginx$`,
				`^kube_cluster_name:`,
				`^orch_cluster_id:`,
				`^kube_namespace:workload-nginx$`,
				`^kube_service:nginx$`,
				`^url:http://`,
			},
		},
	})

	// Test KSM metrics for the nginx deployment
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes_state.deployment.replicas_available",
			Tags: []string{
				"^kube_deployment:nginx$",
				"^kube_namespace:workload-nginx$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^kube_cluster_name:`,
				`^orch_cluster_id:`,
				`^kube_deployment:nginx$`,
				`^kube_namespace:workload-nginx$`,
				`^org:agent-org$`,
				`^team:contp$`,
				`^mail:team-container-platform@datadoghq.com$`,
				`^sub-team:contint$`,
				`^kube_instance_tag:static$`,                            // This is applied via KSM core check instance config
				`^stackid:` + regexp.QuoteMeta(suite.clusterName) + `$`, // Pulumi applies this via DD_TAGS env var
			},
			Value: &testMetricExpectValueArgs{
				Max: 5,
				Min: 1,
			},
		},
	})

	// Test Nginx logs
	suite.testLog(&testLogArgs{
		Filter: testLogFilterArgs{
			Service: "apps-nginx-server",
		},
		Expect: testLogExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:nginx$`,
				`^dirname:/var/log/pods/workload-nginx_nginx-`,
				`^display_container_name:nginx`,
				`^filename:[[:digit:]]+.log$`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source docker image label
				`^image_id:ghcr\.io/datadog/apps-nginx-server@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-nginx-server$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:nginx$`,
				`^kube_deployment:nginx$`,
				`^kube_namespace:workload-nginx$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:nginx-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:nginx-[[:alnum:]]+$`,
				`^kube_service:nginx$`,
				`^pod_name:nginx-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:apps-nginx-server$`,
				`^domain:deployment$`,
				`^mail:team-container-platform@datadoghq.com$`,
				`^org:agent-org$`,
				`^parent-name:nginx$`,
				`^team:contp$`,
			},
			Message: `GET / HTTP/1\.1`,
		},
	})

	// Check HPA is properly scaling up and down
	// This indirectly tests the cluster-agent external metrics server
	suite.testHPA("workload-nginx", "nginx")
}

func (suite *k8sSuite) TestRedis() {
	// `redis` check is auto-configured due to image name
	// Test it is properly scheduled
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "redis.net.instantaneous_ops_per_sec",
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:redis$`,
				`^display_container_name:redis`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source docker image label
				`^image_id:ghcr\.io/datadog/redis@sha256:`,
				`^image_name:ghcr\.io/datadog/redis$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:redis$`,
				`^kube_deployment:redis$`,
				`^kube_namespace:workload-redis$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:redis-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:redis-[[:alnum:]]+$`,
				`^kube_service:redis$`,
				`^pod_name:redis-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:redis$`,
			},
			AcceptUnexpectedTags: true,
		},
	})

	// Test KSM metrics for the redis deployment
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes_state.deployment.replicas_available",
			Tags: []string{
				"^kube_deployment:redis$",
				"^kube_namespace:workload-redis$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^kube_cluster_name:`,
				`^orch_cluster_id:`,
				`^kube_deployment:redis$`,
				`^kube_namespace:workload-redis$`,
				`^kube_instance_tag:static$`,                            // This is applied via KSM core check instance config
				`^stackid:` + regexp.QuoteMeta(suite.clusterName) + `$`, // Pulumi applies this via DD_TAGS env var
			},
			Value: &testMetricExpectValueArgs{
				Max: 5,
				Min: 1,
			},
		},
	})

	// Test Redis logs
	suite.testLog(&testLogArgs{
		Filter: testLogFilterArgs{
			Service: "redis",
		},
		Expect: testLogExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:redis$`,
				`^dirname:/var/log/pods/workload-redis_redis-`,
				`^display_container_name:redis`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source docker image label
				`^filename:[[:digit:]]+.log$`,
				`^image_id:ghcr\.io/datadog/redis@sha256:`,
				`^image_name:ghcr\.io/datadog/redis$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:redis$`,
				`^kube_deployment:redis$`,
				`^kube_namespace:workload-redis$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:redis-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:redis-[[:alnum:]]+$`,
				`^kube_service:redis$`,
				`^pod_name:redis-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:redis$`,
			},
			Message: `Accepted`,
		},
	})

	// Check HPA is properly scaling up and down
	// This indirectly tests the cluster-agent external metrics server
	suite.testHPA("workload-redis", "redis")
}

func (suite *k8sSuite) TestCPU() {
	// TODO: https://datadoghq.atlassian.net/browse/CONTINT-4143
	// Test CPU metrics
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "container.cpu.usage",
			Tags: []string{
				"^kube_deployment:stress-ng$",
				"^kube_namespace:workload-cpustress$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:stress-ng$`,
				`^display_container_name:stress-ng`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-stress-ng@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-stress-ng$`,
				`^image_tag:`,
				`^kube_container_name:stress-ng$`,
				`^kube_deployment:stress-ng$`,
				`^kube_namespace:workload-cpustress$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:stress-ng-[[:alnum:]]+$`,
				`^kube_qos:Guaranteed$`,
				`^kube_replica_set:stress-ng-[[:alnum:]]+$`,
				`^pod_name:stress-ng-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^runtime:containerd$`,
				`^short_image:apps-stress-ng$`,
			},
			Value: &testMetricExpectValueArgs{
				Max: 155000000,
				Min: 145000000,
			},
		},
	})

	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "container.cpu.limit",
			Tags: []string{
				"^kube_deployment:stress-ng$",
				"^kube_namespace:workload-cpustress$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:stress-ng$`,
				`^display_container_name:stress-ng`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-stress-ng@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-stress-ng$`,
				`^image_tag:`,
				`^kube_container_name:stress-ng$`,
				`^kube_deployment:stress-ng$`,
				`^kube_namespace:workload-cpustress$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:stress-ng-[[:alnum:]]+$`,
				`^kube_qos:Guaranteed$`,
				`^kube_replica_set:stress-ng-[[:alnum:]]+$`,
				`^pod_name:stress-ng-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^runtime:containerd$`,
				`^short_image:apps-stress-ng$`,
			},
			Value: &testMetricExpectValueArgs{
				Max: 200000000,
				Min: 200000000,
			},
		},
	})

	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes.cpu.usage.total",
			Tags: []string{
				"^kube_deployment:stress-ng$",
				"^kube_namespace:workload-cpustress$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:stress-ng$`,
				`^display_container_name:stress-ng`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-stress-ng@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-stress-ng$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:stress-ng$`,
				`^kube_deployment:stress-ng$`,
				`^kube_namespace:workload-cpustress$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:stress-ng-[[:alnum:]]+$`,
				`^kube_qos:Guaranteed$`,
				`^kube_replica_set:stress-ng-[[:alnum:]]+$`,
				`^pod_name:stress-ng-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:apps-stress-ng$`,
			},
			Value: &testMetricExpectValueArgs{
				Max: 250000000,
				Min: 75000000,
			},
		},
	})

	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes.cpu.limits",
			Tags: []string{
				"^kube_deployment:stress-ng$",
				"^kube_namespace:workload-cpustress$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:stress-ng$`,
				`^display_container_name:stress-ng`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-stress-ng@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-stress-ng$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:stress-ng$`,
				`^kube_deployment:stress-ng$`,
				`^kube_namespace:workload-cpustress$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:stress-ng-[[:alnum:]]+$`,
				`^kube_qos:Guaranteed$`,
				`^kube_replica_set:stress-ng-[[:alnum:]]+$`,
				`^pod_name:stress-ng-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^short_image:apps-stress-ng$`,
			},
			Value: &testMetricExpectValueArgs{
				Max: 0.2,
				Min: 0.2,
			},
		},
	})
}

func (suite *k8sSuite) TestKSM() {
	// Test VPA metrics for nginx
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes_state.vpa.count",
			Tags: []string{
				"^kube_namespace:workload-nginx$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^kube_cluster_name:` + regexp.QuoteMeta(suite.clusterName) + `$`,
				`^orch_cluster_id:`,
				`^kube_namespace:workload-nginx$`,
				`^org:agent-org$`,
				`^team:contp$`,
				`^mail:team-container-platform@datadoghq.com$`,
				`^kube_instance_tag:static$`,                            // This is applied via KSM core check instance config
				`^stackid:` + regexp.QuoteMeta(suite.clusterName) + `$`, // Pulumi applies this via DD_TAGS env var
			},
			Value: &testMetricExpectValueArgs{
				Max: 1,
				Min: 1,
			},
		},
	})

	// Test VPA metrics for redis
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes_state.vpa.count",
			Tags: []string{
				"^kube_namespace:workload-redis$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^kube_cluster_name:` + regexp.QuoteMeta(suite.clusterName) + `$`,
				`^orch_cluster_id:`,
				`^kube_namespace:workload-redis$`,
				`^kube_instance_tag:static$`,                            // This is applied via KSM core check instance config
				`^stackid:` + regexp.QuoteMeta(suite.clusterName) + `$`, // Pulumi applies this via DD_TAGS env var
			},
			Value: &testMetricExpectValueArgs{
				Max: 1,
				Min: 1,
			},
		},
	})

	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "kubernetes_state_customresource.ddm_value",
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^kube_cluster_name:` + regexp.QuoteMeta(suite.clusterName) + `$`,
				`^orch_cluster_id:`,
				`^customresource_group:datadoghq.com$`,
				`^customresource_version:v1alpha1$`,
				`^customresource_kind:DatadogMetric`,
				`^cr_type:ddm$`,
				`^ddm_namespace:workload-(?:nginx|redis)$`,
				`^ddm_name:(?:nginx|redis)$`,
				`^kube_instance_tag:static$`,                            // This is applied via KSM core check instance config
				`^stackid:` + regexp.QuoteMeta(suite.clusterName) + `$`, // Pulumi applies this via DD_TAGS env var
			},
		},
	})
}

func (suite *k8sSuite) TestDogstatsdInAgent() {
	// Test with UDS
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDS)
	// Test with UDP + Origin detection
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDPOrigin)
	// Test with UDP + DD_ENTITY_ID
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDP)
	// Test with UDP + External Data
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDPExternalData)
	// Test with UDS + CSI Driver
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDSWithCSI)
}

func (suite *k8sSuite) TestDogstatsdStandalone() {
	// Test with UDS
	suite.testDogstatsd(kubeNamespaceDogstatsStandaloneWorkload, kubeDeploymentDogstatsdUDS)
	// Dogstatsd standalone does not support origin detection
	// Test with UDP + DD_ENTITY_ID
	suite.testDogstatsd(kubeNamespaceDogstatsWorkload, kubeDeploymentDogstatsdUDP)
}

func (suite *k8sSuite) testDogstatsd(kubeNamespace, kubeDeployment string) {
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "custom.metric",
			Tags: []string{
				"^kube_deployment:" + regexp.QuoteMeta(kubeDeployment) + "$",
				"^kube_namespace:" + regexp.QuoteMeta(kubeNamespace) + "$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:dogstatsd$`,
				`^display_container_name:dogstatsd`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`, // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source docker image label
				`^image_id:ghcr\.io/datadog/apps-dogstatsd@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-dogstatsd$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:dogstatsd$`,
				`^kube_deployment:` + regexp.QuoteMeta(kubeDeployment) + `$`,
				"^kube_namespace:" + regexp.QuoteMeta(kubeNamespace) + "$",
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:` + regexp.QuoteMeta(kubeDeployment) + `-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:` + regexp.QuoteMeta(kubeDeployment) + `-[[:alnum:]]+$`,
				`^pod_name:` + regexp.QuoteMeta(kubeDeployment) + `-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^series:`,
				`^short_image:apps-dogstatsd$`,
			},
		},
	})
}

func (suite *k8sSuite) TestPrometheus() {
	// Test Prometheus check
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "prom_gauge",
			Tags: []string{
				"^kube_deployment:prometheus$",
				"^kube_namespace:workload-prometheus$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:prometheus$`,
				`^display_container_name:prometheus`,
				`^endpoint:http://.*:8080/metrics$`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`,                                    // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-prometheus@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-prometheus$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:prometheus$`,
				`^kube_deployment:prometheus$`,
				`^kube_namespace:workload-prometheus$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:prometheus-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:prometheus-[[:alnum:]]+$`,
				`^pod_name:prometheus-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^series:`,
				`^short_image:apps-prometheus$`,
			},
		},
	})
}

// This test verifies a metric collected by a Prometheus check using a custom
// configuration stored in etcd. The configuration is identical to the previous
// test, except that the metric "prom_gauge" has been renamed to
// "prom_gauge_configured_in_etcd" to confirm that the check is using the
// etcd-defined configuration.
func (suite *k8sSuite) TestPrometheusWithConfigFromEtcd() {
	suite.testMetric(&testMetricArgs{
		Filter: testMetricFilterArgs{
			Name: "prom_gauge_configured_in_etcd", // This is the name defined in the check config stored in etcd
			Tags: []string{
				"^kube_deployment:prometheus$",
				"^kube_namespace:workload-prometheus$",
			},
		},
		Expect: testMetricExpectArgs{
			Tags: &[]string{
				`^container_id:`,
				`^container_name:prometheus$`,
				`^display_container_name:prometheus`,
				`^endpoint:http://.*:8080/metrics$`,
				`^git\.commit\.sha:[[:xdigit:]]{40}$`,                                    // org.opencontainers.image.revision docker image label
				`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`, // org.opencontainers.image.source   docker image label
				`^image_id:ghcr\.io/datadog/apps-prometheus@sha256:`,
				`^image_name:ghcr\.io/datadog/apps-prometheus$`,
				`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`,
				`^kube_container_name:prometheus$`,
				`^kube_deployment:prometheus$`,
				`^kube_namespace:workload-prometheus$`,
				`^kube_ownerref_kind:replicaset$`,
				`^kube_ownerref_name:prometheus-[[:alnum:]]+$`,
				`^kube_qos:Burstable$`,
				`^kube_replica_set:prometheus-[[:alnum:]]+$`,
				`^pod_name:prometheus-[[:alnum:]]+-[[:alnum:]]+$`,
				`^pod_phase:running$`,
				`^series:`,
				`^short_image:apps-prometheus$`,
			},
		},
	})
}

func (suite *k8sSuite) TestAdmissionControllerWithoutAPMInjection() {
	suite.testAdmissionControllerPod("workload-mutated", "mutated", "", false)
}

func (suite *k8sSuite) TestAdmissionControllerWithLibraryAnnotation() {
	suite.testAdmissionControllerPod("workload-mutated-lib-injection", "mutated-with-lib-annotation", "python", false)
}

func (suite *k8sSuite) TestAdmissionControllerWithAutoDetectedLanguage() {
	suite.testAdmissionControllerPod("workload-mutated-lib-injection", "mutated-with-auto-detected-language", "python", true)
}

func (suite *k8sSuite) testAdmissionControllerPod(namespace string, name string, language string, languageShouldBeAutoDetected bool) {
	ctx := context.Background()

	// When the language should be auto-detected, we need to wait for the
	// deployment to be created and the annotation with the languages to be set
	// by the Cluster Agent so that we can be sure that in the next restart the
	// libraries for the detected language are injected
	if languageShouldBeAutoDetected {

		suite.Require().EventuallyWithTf(func(_ *assert.CollectT) {
			appPod, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("workload-mutated-lib-injection").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", name).String(),
				Limit:         1,
			})

			suite.Require().NoError(err)
			suite.Require().Len(appPod.Items, 1)

			nodeName := appPod.Items[0].Spec.NodeName

			agentPod, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("datadog").List(ctx, metav1.ListOptions{
				LabelSelector: fields.OneTermEqualSelector("app", suite.Env().Agent.LinuxNodeAgent.LabelSelectors["app"]).String(),
				FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
				Limit:         1,
			})

			suite.Require().NoError(err)
			suite.Require().Len(agentPod.Items, 1)

			stdout, _, err := suite.Env().KubernetesCluster.KubernetesClient.PodExec("datadog", agentPod.Items[0].Name, "agent", []string{"agent", "workload-list", "-v"})
			suite.Require().NoError(err)
			suite.Contains(stdout, "Language: python")
			if suite.T().Failed() {
				suite.T().Log(stdout)
			}
		}, 5*time.Minute, 10*time.Second, "Language python was never detected by node agent.")

		suite.Require().EventuallyWithTf(func(c *assert.CollectT) {
			deployment, err := suite.Env().KubernetesCluster.Client().AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if !assert.NoError(c, err) {
				c.Errorf("deployment with name %s in namespace %s not found", name, namespace)
				return
			}

			if deployment.Status.AvailableReplicas == 0 {
				c.Errorf("deployment with name %s in namespace %s has 0 available replicas", name, namespace)
			}

			detectedLangsLabelIsSet := false
			detectedLangsAnnotationRegex := regexp.MustCompile(`^internal\.dd\.datadoghq\.com/.*\.detected_langs$`)
			for annotation := range deployment.Annotations {
				if detectedLangsAnnotationRegex.Match([]byte(annotation)) {
					detectedLangsLabelIsSet = true
					break
				}
			}
			assert.True(c, detectedLangsLabelIsSet)
		}, 5*time.Minute, 10*time.Second, "The deployment with name %s in namespace %s does not exist or does not have the auto detected languages annotation", name, namespace)
	}

	pods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fields.OneTermEqualSelector("app", name).String(),
	})
	suite.Require().NoError(err)
	suite.Require().Len(pods.Items, 1)
	pod := pods.Items[0]

	suite.Require().Len(pod.Spec.Containers, 1)

	// Assert injected env vars
	env := make(map[string]string)
	for _, envVar := range pod.Spec.Containers[0].Env {
		env[envVar.Name] = envVar.Value
	}

	if suite.Contains(env, "DD_DOGSTATSD_URL") {
		suite.Equal("unix:///var/run/datadog/dsd.socket", env["DD_DOGSTATSD_URL"])
	}
	if suite.Contains(env, "DD_TRACE_AGENT_URL") {
		suite.Equal("unix:///var/run/datadog/apm.socket", env["DD_TRACE_AGENT_URL"])
	}
	suite.Contains(env, "DD_ENTITY_ID")
	if suite.Contains(env, "DD_ENV") {
		suite.Equal("e2e", env["DD_ENV"])
	}
	if suite.Contains(env, "DD_SERVICE") {
		suite.Equal(name, env["DD_SERVICE"])
	}
	if suite.Contains(env, "DD_VERSION") {
		suite.Equal("v0.0.1", env["DD_VERSION"])
	}

	// Assert injected volumes and mounts
	hostPathVolumes := make(map[string]*corev1.HostPathVolumeSource)
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil {
			hostPathVolumes[volume.Name] = volume.HostPath
		}
	}

	volumesMarkedAsSafeToEvict := strings.Split(
		pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"], ",",
	)

	if suite.Contains(hostPathVolumes, "datadog") {
		suite.Equal("/var/run/datadog", hostPathVolumes["datadog"].Path)
		suite.Contains(volumesMarkedAsSafeToEvict, "datadog")
	}

	volumeMounts := make(map[string][]string)
	for _, volumeMount := range pod.Spec.Containers[0].VolumeMounts {
		volumeMounts[volumeMount.Name] = append(volumeMounts[volumeMount.Name], volumeMount.MountPath)
	}

	if suite.Contains(volumeMounts, "datadog") {
		suite.ElementsMatch([]string{"/var/run/datadog"}, volumeMounts["datadog"])
	}

	switch language {
	// APM supports several languages, but for now all the test apps are Python
	case "python":
		emptyDirVolumes := make(map[string]*corev1.EmptyDirVolumeSource)
		for _, volume := range pod.Spec.Volumes {
			if volume.EmptyDir != nil {
				emptyDirVolumes[volume.Name] = volume.EmptyDir
			}
		}

		if suite.Contains(emptyDirVolumes, "datadog-auto-instrumentation") {
			suite.Contains(volumesMarkedAsSafeToEvict, "datadog-auto-instrumentation")
		}

		if suite.Contains(emptyDirVolumes, "datadog-auto-instrumentation-etc") {
			suite.Contains(volumesMarkedAsSafeToEvict, "datadog-auto-instrumentation-etc")
		}

		if suite.Contains(volumeMounts, "datadog-auto-instrumentation") {
			suite.ElementsMatch([]string{
				"/opt/datadog-packages/datadog-apm-inject",
				"/opt/datadog/apm/library",
			}, volumeMounts["datadog-auto-instrumentation"])
		}
	}

}

func (suite *k8sSuite) TestContainerImage() {
	sendEvent := func(alertType, text string) {
		if _, err := suite.DatadogClient().PostEvent(&datadog.Event{
			Title: pointer.Ptr(suite.T().Name()),
			Text: pointer.Ptr(fmt.Sprintf(`%%%%%%
`+"```"+`
%s
`+"```"+`
 %%%%%%`, text)),
			AlertType: &alertType,
			Tags: []string{
				"app:agent-new-e2e-tests-containers",
				"cluster_name:" + suite.clusterName,
				"contimage:ghcr.io/datadog/apps-nginx-server",
				"test:" + suite.T().Name(),
			},
		}); err != nil {
			suite.T().Logf("Failed to post event: %s", err)
		}
	}

	defer func() {
		if suite.T().Failed() {
			sendEvent("error", "Failed finding the `ghcr.io/datadog/apps-nginx-server` container image payload with proper tags")
		} else {
			sendEvent("success", "All good!")
		}
	}()

	suite.EventuallyWithTf(func(collect *assert.CollectT) {
		c := &myCollectT{
			CollectT: collect,
			errors:   []error{},
		}
		// To enforce the use of myCollectT instead
		collect = nil //nolint:ineffassign

		defer func() {
			if len(c.errors) == 0 {
				sendEvent("success", "All good!")
			} else {
				sendEvent("warning", errors.Join(c.errors...).Error())
			}
		}()

		images, err := suite.Fakeintake.FilterContainerImages("ghcr.io/datadog/apps-nginx-server")
		// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NoErrorf(c, err, "Failed to query fake intake") {
			return
		}
		// Can be replaced by require.NoEmptyf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NotEmptyf(c, images, "No container_image yet") {
			return
		}

		expectedTags := []*regexp.Regexp{
			regexp.MustCompile(`^architecture:(amd|arm)64$`),
			regexp.MustCompile(`^git\.commit\.sha:[[:xdigit:]]{40}$`),
			regexp.MustCompile(`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`),
			regexp.MustCompile(`^image_id:ghcr\.io/datadog/apps-nginx-server@sha256:`),
			regexp.MustCompile(`^image_name:ghcr\.io/datadog/apps-nginx-server$`),
			regexp.MustCompile(`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`),
			regexp.MustCompile(`^os_name:linux$`),
			regexp.MustCompile(`^short_image:apps-nginx-server$`),
		}
		err = assertTags(images[len(images)-1].GetTags(), expectedTags, []*regexp.Regexp{}, false)
		assert.NoErrorf(c, err, "Tags mismatch")
	}, 2*time.Minute, 10*time.Second, "Failed finding the container image payload")
}

func (suite *k8sSuite) TestSBOM() {
	sendEvent := func(alertType, text string) {
		if _, err := suite.DatadogClient().PostEvent(&datadog.Event{
			Title: pointer.Ptr(suite.T().Name()),
			Text: pointer.Ptr(fmt.Sprintf(`%%%%%%
`+"```"+`
%s
`+"```"+`
 %%%%%%`, text)),
			AlertType: &alertType,
			Tags: []string{
				"app:agent-new-e2e-tests-containers",
				"cluster_name:" + suite.clusterName,
				"sbom:ghcr.io/datadog/apps-nginx-server",
				"test:" + suite.T().Name(),
			},
		}); err != nil {
			suite.T().Logf("Failed to post event: %s", err)
		}
	}

	defer func() {
		if suite.T().Failed() {
			sendEvent("error", "Failed finding the `ghcr.io/datadog/apps-nginx-server` SBOM payload with proper tags")
		} else {
			sendEvent("success", "All good!")
		}
	}()

	suite.EventuallyWithTf(func(collect *assert.CollectT) {
		c := &myCollectT{
			CollectT: collect,
			errors:   []error{},
		}
		// To enforce the use of myCollectT instead
		collect = nil //nolint:ineffassign

		defer func() {
			if len(c.errors) == 0 {
				sendEvent("success", "All good!")
			} else {
				sendEvent("warning", errors.Join(c.errors...).Error())
			}
		}()

		sbomIDs, err := suite.Fakeintake.GetSBOMIDs()
		// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NoErrorf(c, err, "Failed to query fake intake") {
			return
		}

		sbomIDs = lo.Filter(sbomIDs, func(id string, _ int) bool {
			return strings.HasPrefix(id, "ghcr.io/datadog/apps-nginx-server")
		})

		// Can be replaced by require.NoEmptyf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NotEmptyf(c, sbomIDs, "No SBOM for ghcr.io/datadog/apps-nginx-server yet") {
			return
		}

		images := lo.FlatMap(sbomIDs, func(id string, _ int) []*aggregator.SBOMPayload {
			images, err := suite.Fakeintake.FilterSBOMs(id)
			assert.NoErrorf(c, err, "Failed to query fake intake")
			return images
		})

		// Can be replaced by require.NoEmptyf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NotEmptyf(c, images, "No SBOM payload yet") {
			return
		}

		images = lo.Filter(images, func(image *aggregator.SBOMPayload, _ int) bool {
			return image.Status == sbom.SBOMStatus_SUCCESS
		})

		// Can be replaced by require.NoEmptyf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NotEmptyf(c, images, "No successful SBOM yet") {
			return
		}

		images = lo.Filter(images, func(image *aggregator.SBOMPayload, _ int) bool {
			cyclonedx := image.GetCyclonedx()
			return cyclonedx != nil &&
				cyclonedx.Metadata != nil &&
				cyclonedx.Metadata.Component != nil
		})

		// Can be replaced by require.NoEmptyf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NotEmptyf(c, images, "No SBOM with complete CycloneDX") {
			return
		}

		for _, image := range images {
			if !assert.NotNil(c, image.GetCyclonedx().Metadata.Component.Properties) {
				continue
			}

			expectedTags := []*regexp.Regexp{
				regexp.MustCompile(`^architecture:(amd|arm)64$`),
				regexp.MustCompile(`^git\.commit\.sha:[[:xdigit:]]{40}$`),
				regexp.MustCompile(`^git\.repository_url:https://github\.com/DataDog/test-infra-definitions$`),
				regexp.MustCompile(`^image_id:ghcr\.io/datadog/apps-nginx-server@sha256:`),
				regexp.MustCompile(`^image_name:ghcr\.io/datadog/apps-nginx-server$`),
				regexp.MustCompile(`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`),
				regexp.MustCompile(`^os_name:linux$`),
				regexp.MustCompile(`^short_image:apps-nginx-server$`),
			}
			err = assertTags(image.GetTags(), expectedTags, []*regexp.Regexp{}, false)
			assert.NoErrorf(c, err, "Tags mismatch")

			properties := lo.Associate(image.GetCyclonedx().Metadata.Component.Properties, func(property *cyclonedx_v1_4.Property) (string, string) {
				return property.Name, *property.Value
			})

			if assert.Contains(c, properties, "aquasecurity:trivy:RepoTag") {
				assert.Equal(c, "ghcr.io/datadog/apps-nginx-server:"+apps.Version, properties["aquasecurity:trivy:RepoTag"])
			}

			if assert.Contains(c, properties, "aquasecurity:trivy:RepoDigest") {
				assert.Contains(c, properties["aquasecurity:trivy:RepoDigest"], "ghcr.io/datadog/apps-nginx-server@sha256:")
			}
		}
	}, 2*time.Minute, 10*time.Second, "Failed finding the container image payload")
}

func (suite *k8sSuite) TestContainerLifecycleEvents() {
	sendEvent := func(alertType, text string) {
		if _, err := suite.DatadogClient().PostEvent(&datadog.Event{
			Title: pointer.Ptr(suite.T().Name()),
			Text: pointer.Ptr(fmt.Sprintf(`%%%%%%
`+"```"+`
%s
`+"```"+`
 %%%%%%`, text)),
			AlertType: &alertType,
			Tags: []string{
				"app:agent-new-e2e-tests-containers",
				"cluster_name:" + suite.clusterName,
				"contlcycle:ghcr.io/datadog/apps-nginx-server",
				"test:" + suite.T().Name(),
			},
		}); err != nil {
			suite.T().Logf("Failed to post event: %s", err)
		}
	}

	defer func() {
		if suite.T().Failed() {
			sendEvent("error", "Failed finding the `ghcr.io/datadog/apps-nginx-server` container lifecycle event")
		} else {
			sendEvent("success", "All good!")
		}
	}()

	var nginxPod corev1.Pod

	suite.Require().EventuallyWithTf(func(c *assert.CollectT) {
		pods, err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("workload-nginx").List(context.Background(), metav1.ListOptions{
			LabelSelector: fields.OneTermEqualSelector("app", "nginx").String(),
			FieldSelector: fields.OneTermEqualSelector("status.phase", "Running").String(),
		})
		// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NoErrorf(c, err, "Failed to list nginx pods") {
			return
		}
		if !assert.NotEmptyf(c, pods.Items, "Failed to find an nginx pod") {
			return
		}

		// Choose the oldest pod.
		// If we choose a pod that is too recent, there is a risk that we delete a pod that hasn’t been seen by the agent yet.
		// So that no pod lifecycle event is sent.
		nginxPod = lo.MinBy(pods.Items, func(item corev1.Pod, min corev1.Pod) bool {
			return item.Status.StartTime.Before(min.Status.StartTime)
		})
	}, 1*time.Minute, 10*time.Second, "Failed to find an nginx pod")

	err := suite.Env().KubernetesCluster.Client().CoreV1().Pods("workload-nginx").Delete(context.Background(), nginxPod.Name, metav1.DeleteOptions{})
	suite.Require().NoError(err)

	suite.EventuallyWithTf(func(collect *assert.CollectT) {
		c := &myCollectT{
			CollectT: collect,
			errors:   []error{},
		}
		// To enforce the use of myCollectT instead
		collect = nil //nolint:ineffassign

		defer func() {
			if len(c.errors) == 0 {
				sendEvent("success", "All good!")
			} else {
				sendEvent("warning", errors.Join(c.errors...).Error())
			}
		}()

		events, err := suite.Fakeintake.GetContainerLifecycleEvents()
		// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NoErrorf(c, err, "Failed to query fake intake") {
			return
		}

		foundPodEvent := false

		for _, event := range events {
			if podEvent := event.GetPod(); podEvent != nil {
				if types.UID(podEvent.GetPodUID()) == nginxPod.UID {
					foundPodEvent = true
					break
				}
			}
		}

		assert.Truef(c, foundPodEvent, "Failed to find the pod lifecycle event for pod %s/%s", nginxPod.Namespace, nginxPod.Name)
	}, 2*time.Minute, 10*time.Second, "Failed to find the pod lifecycle event for pod %s/%s", nginxPod.Namespace, nginxPod.Name)
}

func (suite *k8sSuite) testHPA(namespace, deployment string) {
	suite.Run(fmt.Sprintf("hpa   kubernetes_state.deployment.replicas_available{kube_namespace:%s,kube_deployment:%s}", namespace, deployment), func() {
		sendEvent := func(alertType, text string, time *int) {
			if _, err := suite.DatadogClient().PostEvent(&datadog.Event{
				Title: pointer.Ptr(fmt.Sprintf("testHPA %s/%s", namespace, deployment)),
				Text: pointer.Ptr(fmt.Sprintf(`%%%%%%
%s
 %%%%%%`, text)),
				Time:      time,
				AlertType: &alertType,
				Tags: []string{
					"app:agent-new-e2e-tests-containers",
					"cluster_name:" + suite.clusterName,
					"metric:kubernetes_state.deployment.replicas_available",
					"filter_tag_kube_namespace:" + namespace,
					"filter_tag_kube_deployment:" + deployment,
					"test:" + suite.T().Name(),
				},
			}); err != nil {
				suite.T().Logf("Failed to post event: %s", err)
			}
		}

		defer func() {
			if suite.T().Failed() {
				sendEvent("error", "Failed to witness scale up *and* scale down events.", nil)
			} else {
				sendEvent("success", "Scale up and scale down events detected.", nil)
			}
		}()

		suite.EventuallyWithTf(func(c *assert.CollectT) {
			metrics, err := suite.Fakeintake.FilterMetrics(
				"kubernetes_state.deployment.replicas_available",
				fakeintake.WithTags[*aggregator.MetricSeries]([]string{
					"kube_namespace:" + namespace,
					"kube_deployment:" + deployment,
				}),
			)
			// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
			if !assert.NoErrorf(c, err, "Failed to query fake intake") {
				return
			}
			if !assert.NotEmptyf(c, metrics, "No `kubernetes_state.deployment.replicas_available{kube_namespace:%s,kube_deployment:%s}` metrics yet", namespace, deployment) {
				sendEvent("warning", fmt.Sprintf("No `kubernetes_state.deployment.replicas_available{kube_namespace:%s,kube_deployment:%s}` metrics yet", namespace, deployment), nil)
				return
			}

			// Check HPA is properly scaling up and down
			// This indirectly tests the cluster-agent external metrics server
			scaleUp := false
			scaleDown := false
			prevValue := -1.0
		out:
			for _, metric := range metrics {
				for _, point := range metric.GetPoints() {
					if prevValue == -1.0 {
						prevValue = point.Value
						continue
					}

					if !scaleUp && point.Value > prevValue+0.5 {
						scaleUp = true
						sendEvent("success", "Scale up detected.", pointer.Ptr(int(point.Timestamp)))
						if scaleDown {
							break out
						}
					} else if !scaleDown && point.Value < prevValue-0.5 {
						scaleDown = true
						sendEvent("success", "Scale down detected.", pointer.Ptr(int(point.Timestamp)))
						if scaleUp {
							break out
						}
					}
					prevValue = point.Value
				}
			}
			assert.Truef(c, scaleUp, "No scale up detected")
			assert.Truef(c, scaleDown, "No scale down detected")
			// The deployments that have an HPA configured (nginx and redis)
			// exhibit a traffic pattern that follows a sine wave with a
			// 20-minute period. This is defined in the test-infra-definitions
			// repo. For this reason, the timeout for this test needs to be a
			// bit higher than 20 min to capture the scale down event.
		}, 25*time.Minute, 10*time.Second, "Failed to witness scale up and scale down of %s.%s", namespace, deployment)
	})
}

func (suite *k8sSuite) TestTraceUDS() {
	suite.testTrace(kubeDeploymentTracegenUDSWorkload)
}

func (suite *k8sSuite) TestTraceTCP() {
	suite.testTrace(kubeDeploymentTracegenTCPWorkload)
}

// testTrace verifies that traces are tagged with container and pod tags.
func (suite *k8sSuite) testTrace(kubeDeployment string) {
	suite.EventuallyWithTf(func(c *assert.CollectT) {
		traces, cerr := suite.Fakeintake.GetTraces()
		// Can be replaced by require.NoErrorf(…) once https://github.com/stretchr/testify/pull/1481 is merged
		if !assert.NoErrorf(c, cerr, "Failed to query fake intake") {
			return
		}

		var err error
		// Iterate starting from the most recent traces
		for _, trace := range traces {
			tags := lo.MapToSlice(trace.Tags, func(k string, v string) string {
				return k + ":" + v
			})
			// Assert origin detection is working properly
			err = assertTags(tags, []*regexp.Regexp{
				regexp.MustCompile(`^container_id:`),
				regexp.MustCompile(`^container_name:` + kubeDeployment + `$`),
				regexp.MustCompile(`^display_container_name:` + kubeDeployment + `_` + kubeDeployment + `-[[:alnum:]]+-[[:alnum:]]+$`),
				regexp.MustCompile(`^git\.commit\.sha:[[:xdigit:]]{40}$`),
				regexp.MustCompile(`^git.repository_url:https://github.com/DataDog/test-infra-definitions$`),
				regexp.MustCompile(`^image_id:`), // field is inconsistent. it can be a hash or an image + hash
				regexp.MustCompile(`^image_name:ghcr\.io/datadog/apps-tracegen$`),
				regexp.MustCompile(`^image_tag:` + regexp.QuoteMeta(apps.Version) + `$`),
				regexp.MustCompile(`^kube_container_name:` + kubeDeployment + `$`),
				regexp.MustCompile(`^kube_deployment:` + kubeDeployment + `$`),
				regexp.MustCompile(`^kube_namespace:` + kubeNamespaceTracegenWorkload + `$`),
				regexp.MustCompile(`^kube_ownerref_kind:replicaset$`),
				regexp.MustCompile(`^kube_ownerref_name:` + kubeDeployment + `-[[:alnum:]]+$`),
				regexp.MustCompile(`^kube_replica_set:` + kubeDeployment + `-[[:alnum:]]+$`),
				regexp.MustCompile(`^kube_qos:burstable$`),
				regexp.MustCompile(`^pod_name:` + kubeDeployment + `-[[:alnum:]]+-[[:alnum:]]+$`),
				regexp.MustCompile(`^pod_phase:running$`),
				regexp.MustCompile(`^short_image:apps-tracegen$`),
			}, []*regexp.Regexp{}, false)
			if err == nil {
				break
			}
		}
		require.NoErrorf(c, err, "Failed finding trace with proper tags")
	}, 2*time.Minute, 10*time.Second, "Failed finding trace with proper tags")
}
