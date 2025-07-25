// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package djm contains data-jobs-monitoring installation logic
package djm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/DataDog/datadog-agent/pkg/fleet/installer/setup/common"
	"github.com/DataDog/datadog-agent/pkg/fleet/installer/setup/config"
	"github.com/DataDog/datadog-agent/pkg/fleet/installer/telemetry"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	databricksInjectorVersion   = "0.43.1-1"
	databricksJavaTracerVersion = "1.51.1-1"
	databricksAgentVersion      = "7.68.2-1"
	fetchTimeoutDuration        = 5 * time.Second
	gpuIntegrationRestartDelay  = 60 * time.Second
	restartLogFile              = "/var/log/datadog-gpu-restart"
)

var (
	jobNameRegex       = regexp.MustCompile(`[,\']+`)
	clusterNameRegex   = regexp.MustCompile(`[^a-zA-Z0-9_:.-]+`)
	workspaceNameRegex = regexp.MustCompile(`[^a-zA-Z0-9_:.-]+`)
	driverLogs         = []config.IntegrationConfigLogs{
		{
			Type:                   "file",
			Path:                   "/databricks/driver/logs/*.log",
			Source:                 "driver_logs",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
		{
			Type:                   "file",
			Path:                   "/databricks/driver/logs/stderr",
			Source:                 "driver_stderr",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
		{
			Type:                   "file",
			Path:                   "/databricks/driver/logs/stdout",
			Source:                 "driver_stdout",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
	}
	workerLogs = []config.IntegrationConfigLogs{
		{
			Type:                   "file",
			Path:                   "/databricks/spark/work/*/*/*.log",
			Source:                 "worker_logs",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
		{
			Type:                   "file",
			Path:                   "/databricks/spark/work/*/*/stderr",
			Source:                 "worker_stderr",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
		{
			Type:                   "file",
			Path:                   "/databricks/spark/work/*/*/stdout",
			Source:                 "worker_stdout",
			Service:                "databricks",
			AutoMultiLineDetection: true,
		},
	}
	tracerConfigDatabricks = config.APMConfigurationDefault{
		DataJobsEnabled:     config.BoolToPtr(true),
		IntegrationsEnabled: config.BoolToPtr(false),
	}
)

// SetupDatabricks sets up the Databricks environment
func SetupDatabricks(s *common.Setup) error {
	s.Packages.Install(common.DatadogAgentPackage, databricksAgentVersion)
	s.Packages.Install(common.DatadogAPMInjectPackage, databricksInjectorVersion)
	s.Packages.Install(common.DatadogAPMLibraryJavaPackage, databricksJavaTracerVersion)

	s.Out.WriteString("Applying specific Data Jobs Monitoring config\n")
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("failed to get hostname: %w", err)
	}
	s.Config.DatadogYAML.Hostname = hostname
	s.Config.DatadogYAML.DJM.Enabled = true
	s.Config.DatadogYAML.ExpectedTagsDuration = "10m"
	s.Config.DatadogYAML.ProcessConfig.ExpvarPort = 6063 // avoid port conflict on 6062

	if os.Getenv("DD_TRACE_DEBUG") == "true" {
		s.Out.WriteString("Enabling Datadog Java Tracer DEBUG logs on DD_TRACE_DEBUG=true\n")
		tracerConfigDatabricks.TraceDebug = config.BoolToPtr(true)
	}
	s.Config.ApplicationMonitoringYAML = &config.ApplicationMonitoringConfig{
		Default: tracerConfigDatabricks,
	}

	setupCommonHostTags(s)
	fetchDatabricksCustomTags(s)
	installMethod := "manual"
	if os.Getenv("DD_DJM_INIT_IS_MANAGED_INSTALL") == "true" {
		installMethod = "managed"
	}
	s.Span.SetTag("install_method", installMethod)

	if os.Getenv("DD_GPU_MONITORING_ENABLED") == "true" {
		setupGPUIntegration(s)
	}

	switch os.Getenv("DB_IS_DRIVER") {
	case "TRUE":
		setupDatabricksDriver(s)
	default:
		setupDatabricksWorker(s)
	}
	if s.Config.DatadogYAML.LogsEnabled {
		loadLogProcessingRules(s)
	}
	return nil
}

func setupCommonHostTags(s *common.Setup) {
	setIfExists(s, "DB_DRIVER_IP", "spark_host_ip", nil)
	setIfExists(s, "DB_INSTANCE_TYPE", "databricks_instance_type", nil)
	setClearIfExists(s, "DB_IS_JOB_CLUSTER", "databricks_is_job_cluster", nil)
	setClearIfExists(s, "DATABRICKS_RUNTIME_VERSION", "databricks_runtime", nil)
	setClearIfExists(s, "SPARK_SCALA_VERSION", "scala_version", nil)
	setIfExists(s, "DD_JOB_NAME", "job_name", func(v string) string {
		return jobNameRegex.ReplaceAllString(v, "_")
	})

	// duplicated for backward compatibility
	setIfExists(s, "DB_CLUSTER_NAME", "databricks_cluster_name", func(v string) string {
		return clusterNameRegex.ReplaceAllString(v, "_")
	})
	setIfExists(s, "DB_CLUSTER_ID", "databricks_cluster_id", nil)

	setIfExists(s, "DATABRICKS_WORKSPACE", "databricks_workspace", nil)
	setClearIfExists(s, "DATABRICKS_WORKSPACE", "workspace", func(v string) string {
		v = strings.ToLower(v)
		v = strings.Trim(v, "\"'")
		return workspaceNameRegex.ReplaceAllString(v, "_")
	})

	setClearIfExists(s, "DB_CLUSTER_ID", "cluster_id", nil)
	setIfExists(s, "DB_CLUSTER_NAME", "cluster_name", func(v string) string {
		return clusterNameRegex.ReplaceAllString(v, "_")
	})

	jobID, runID, ok := getJobAndRunIDs()
	if ok {
		setHostTag(s, "jobid", jobID)
		setHostTag(s, "runid", runID)
		setHostTag(s, "dd.internal.resource:databricks_job", jobID)
	}
	setHostTag(s, "data_workload_monitoring_trial", "true")

	// Set databricks_cluster resource tag based on whether we're on a job cluster
	isJobCluster, _ := os.LookupEnv("DB_IS_JOB_CLUSTER")
	if isJobCluster == "TRUE" && ok {
		setHostTag(s, "dd.internal.resource:databricks_cluster", jobID)
	} else {
		setIfExists(s, "DB_CLUSTER_ID", "dd.internal.resource:databricks_cluster", nil)
	}

	addCustomHostTags(s)
}

func getJobAndRunIDs() (jobID, runID string, ok bool) {
	clusterName := os.Getenv("DB_CLUSTER_NAME")
	if !strings.HasPrefix(clusterName, "job-") {
		return "", "", false
	}
	if !strings.Contains(clusterName, "-run-") {
		return "", "", false
	}
	parts := strings.Split(clusterName, "-")
	if len(parts) < 4 {
		return "", "", false
	}
	if parts[0] != "job" || parts[2] != "run" {
		return "", "", false
	}
	return parts[1], parts[3], true
}

func setIfExists(s *common.Setup, envKey, tagKey string, normalize func(string) string) {
	value, ok := os.LookupEnv(envKey)
	if !ok {
		return
	}
	if normalize != nil {
		value = normalize(value)
	}
	setHostTag(s, tagKey, value)
}

func setClearIfExists(s *common.Setup, envKey, tagKey string, normalize func(string) string) {
	value, ok := os.LookupEnv(envKey)
	if !ok {
		return
	}
	if normalize != nil {
		value = normalize(value)
	}
	setClearHostTag(s, tagKey, value)
}

func setHostTag(s *common.Setup, tagKey, value string) {
	s.Config.DatadogYAML.Tags = append(s.Config.DatadogYAML.Tags, tagKey+":"+value)
	isTagPresent := "false"
	if value != "" {
		isTagPresent = "true"
	}
	s.Span.SetTag("host_tag_set."+tagKey, isTagPresent)
}

func setClearHostTag(s *common.Setup, tagKey, value string) {
	s.Config.DatadogYAML.Tags = append(s.Config.DatadogYAML.Tags, tagKey+":"+value)
	s.Span.SetTag("host_tag_value."+tagKey, value)
}

// setupGPUIntegration configures GPU monitoring integration
func setupGPUIntegration(s *common.Setup) {
	s.Out.WriteString("Setting up GPU monitoring based on env variable GPU_MONITORING_ENABLED=true\n")

	s.Config.DatadogYAML.CollectGPUTags = true
	s.Config.DatadogYAML.EnableNVMLDetection = true

	if s.Config.SystemProbeYAML == nil {
		s.Config.SystemProbeYAML = &config.SystemProbeConfig{}
	}
	s.Config.SystemProbeYAML.GPUMonitoringConfig = config.GPUMonitoringConfig{
		Enabled: true,
	}

	s.Span.SetTag("host_tag_set.gpu_monitoring_enabled", "true")

	// Agent must be restarted after NVML initialization, which occurs after init script execution
	s.DelayedAgentRestartConfig.Scheduled = true
	s.DelayedAgentRestartConfig.Delay = gpuIntegrationRestartDelay
	s.DelayedAgentRestartConfig.LogFile = restartLogFile
}

func setupDatabricksDriver(s *common.Setup) {
	s.Out.WriteString("Setting up Spark integration config on the Driver\n")
	setClearHostTag(s, "spark_node", "driver")

	var sparkIntegration config.IntegrationConfig
	if os.Getenv("DRIVER_LOGS_ENABLED") == "true" {
		s.Config.DatadogYAML.LogsEnabled = true
		sparkIntegration.Logs = driverLogs
		s.Span.SetTag("host_tag_set.driver_logs_enabled", "true")
	}
	if os.Getenv("DB_DRIVER_IP") != "" {
		sparkIntegration.Instances = []any{
			config.IntegrationConfigInstanceSpark{
				SparkURL:         "http://" + os.Getenv("DB_DRIVER_IP") + ":40001",
				SparkClusterMode: "spark_driver_mode",
				ClusterName:      os.Getenv("DB_CLUSTER_NAME"),
				StreamingMetrics: true,
			},
		}
	} else {
		log.Warn("DB_DRIVER_IP not set")
	}
	s.Config.IntegrationConfigs["spark.d/databricks.yaml"] = sparkIntegration
}

func setupDatabricksWorker(s *common.Setup) {
	setClearHostTag(s, "spark_node", "worker")

	if os.Getenv("WORKER_LOGS_ENABLED") == "true" {
		var sparkIntegration config.IntegrationConfig
		s.Config.DatadogYAML.LogsEnabled = true
		sparkIntegration.Logs = workerLogs
		s.Span.SetTag("host_tag_set.worker_logs_enabled", "true")
		s.Config.IntegrationConfigs["spark.d/databricks.yaml"] = sparkIntegration
	}
}

func addCustomHostTags(s *common.Setup) {
	tags := os.Getenv("DD_TAGS")
	extraTags := os.Getenv("DD_EXTRA_TAGS")

	// Split by comma or space because agent uses space and old script uses comma
	tagsArray := strings.FieldsFunc(tags, func(r rune) bool {
		return r == ',' || r == ' '
	})
	extraTagsArray := strings.FieldsFunc(extraTags, func(r rune) bool {
		return r == ',' || r == ' '
	})

	for _, tag := range tagsArray {
		if tag != "" {
			s.Config.DatadogYAML.Tags = append(s.Config.DatadogYAML.Tags, tag)
		}
	}
	for _, tag := range extraTagsArray {
		if tag != "" {
			s.Config.DatadogYAML.ExtraTags = append(s.Config.DatadogYAML.ExtraTags, tag)
		}
	}
	s.Span.SetTag("host_tag_set.dd_tags", len(tagsArray))
	s.Span.SetTag("host_tag_set.dd_extra_tags", len(extraTagsArray))
}

func parseLogProcessingRules(input string) ([]config.LogProcessingRule, error) {
	var rules []config.LogProcessingRule
	// single quote are invalid for string in json
	jsonInput := strings.ReplaceAll(input, `'`, `"`)
	err := json.Unmarshal([]byte(jsonInput), &rules)
	if err != nil {
		return nil, err
	}
	return rules, nil
}

func loadLogProcessingRules(s *common.Setup) {
	if rawRules := os.Getenv("DD_LOGS_CONFIG_PROCESSING_RULES"); rawRules != "" {
		processingRules, err := parseLogProcessingRules(rawRules)
		if err != nil {
			log.Warnf("Failed to parse log processing rules: %v", err)
			s.Out.WriteString(fmt.Sprintf("Invalid log processing rules: %v\n", err))
		} else {
			logsConfig := config.LogsConfig{ProcessingRules: processingRules}
			s.Config.DatadogYAML.LogsConfig = logsConfig
			s.Out.WriteString(fmt.Sprintf("Loaded %d log processing rule(s) from DD_LOGS_CONFIG_PROCESSING_RULES\n", len(processingRules)))
			s.Span.SetTag("host_tag_set.log_rules", len(processingRules))
		}
	}
}

// ClusterTags are custom tags from Databricks API response
type ClusterTags struct {
	CustomTags   map[string]string `json:"custom_tags"`
	SparkVersion string            `json:"spark_version"`
}

// JobTags custom tags from Databricks API response
type JobTags struct {
	Settings struct {
		Tags map[string]string `json:"tags"`
	} `json:"settings"`
}

// Fetch Cluster custom tags and Job custom tags from the databricks API
// Will only do requests if the API key and Hostname are present
// It should not do requests without those, it should not panic if the HTTP requests are failing.
// It should add the custom tags to the datadog-agent tags config.
func fetchDatabricksCustomTags(s *common.Setup) {
	token := os.Getenv("DATABRICKS_TOKEN")
	host := os.Getenv("DATABRICKS_HOST")
	if token == "" || host == "" {
		s.Span.SetTag("databricks_api_auth.provided", "false")
		s.Out.WriteString("DATABRICKS_TOKEN or DATABRICKS_HOST not set, skipping custom tags fetch\n")
		return
	}

	s.Out.WriteString("Fetching custom tags from Databricks API\n")

	client := &http.Client{
		Timeout: fetchTimeoutDuration,
	}

	clusterID := os.Getenv("DB_CLUSTER_ID")
	if clusterID == "" {
		s.Out.WriteString("DB_CLUSTER_ID not set, skipping cluster tags fetch\n")
	} else {
		clusterTags, err := fetchClusterTagsFunc(client, host, token, clusterID, s)
		if err != nil {
			s.Out.WriteString(fmt.Sprintf("Failed to fetch cluster tags: %v\n", err))
		} else {
			addTagsToConfig(s, clusterTags)
		}
	}

	jobID, _, ok := getJobAndRunIDs()
	if !ok || jobID == "" {
		s.Out.WriteString("No valid job ID found, skipping job tags fetch\n")
		return
	}

	jobTags, err := fetchJobTagsFunc(client, host, token, jobID, s)
	if err != nil {
		s.Out.WriteString(fmt.Sprintf("Failed to fetch job tags: %v\n", err))
		return
	}

	addTagsToConfig(s, jobTags)
}

// Variables to hold the original functions so we can mock them for testing
var (
	fetchClusterTagsFunc = fetchClusterTags
	fetchJobTagsFunc     = fetchJobTags
)

func fetchClusterTags(client *http.Client, host, token, clusterID string, s *common.Setup) (map[string]string, error) {
	var err error
	span, _ := telemetry.StartSpanFromContext(s.Ctx, "fetch.cluster.custom_tags")
	defer func() { span.Finish(err) }()

	url := fmt.Sprintf("%s/api/2.1/clusters/get?cluster_id=%s", host, clusterID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var clusterResponse ClusterTags
	err = json.Unmarshal(body, &clusterResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return clusterResponse.CustomTags, nil
}

func fetchJobTags(client *http.Client, host, token, jobID string, s *common.Setup) (map[string]string, error) {
	var err error
	span, _ := telemetry.StartSpanFromContext(s.Ctx, "fetch.job.custom_tags")
	defer func() { span.Finish(err) }()

	url := fmt.Sprintf("%s/api/2.1/jobs/get?job_id=%s", host, jobID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var jobResponse JobTags
	err = json.Unmarshal(body, &jobResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return jobResponse.Settings.Tags, nil
}

func addTagsToConfig(s *common.Setup, tags map[string]string) {
	if len(tags) == 0 {
		return
	}

	for key, value := range tags {
		tagString := fmt.Sprintf("%s:%s", key, value)
		s.Config.DatadogYAML.Tags = append(s.Config.DatadogYAML.Tags, tagString)

		s.Span.SetTag("host_tag_set."+key, value)
	}
}
