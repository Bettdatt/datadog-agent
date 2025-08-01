// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package remoteagentregistryimpl implements the remoteagentregistry component interface
package remoteagentregistryimpl

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"

	"github.com/DataDog/datadog-agent/comp/core/config"
	flarebuilder "github.com/DataDog/datadog-agent/comp/core/flare/builder"
	flaretypes "github.com/DataDog/datadog-agent/comp/core/flare/types"
	remoteagentregistry "github.com/DataDog/datadog-agent/comp/core/remoteagentregistry/def"
	raproto "github.com/DataDog/datadog-agent/comp/core/remoteagentregistry/proto"
	remoteagentregistryStatus "github.com/DataDog/datadog-agent/comp/core/remoteagentregistry/status"
	util "github.com/DataDog/datadog-agent/comp/core/remoteagentregistry/util"
	"github.com/DataDog/datadog-agent/comp/core/status"
	"github.com/DataDog/datadog-agent/comp/core/telemetry"
	compdef "github.com/DataDog/datadog-agent/comp/def"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/core"
	ddgrpc "github.com/DataDog/datadog-agent/pkg/util/grpc"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// Requires defines the dependencies for the remoteagentregistry component
type Requires struct {
	Config    config.Component
	Lifecycle compdef.Lifecycle
	Telemetry telemetry.Component
}

// Provides defines the output of the remoteagentregistry component
type Provides struct {
	Comp          remoteagentregistry.Component
	FlareProvider flaretypes.Provider
	Status        status.InformationProvider
}

// NewComponent creates a new remoteagent component
func NewComponent(reqs Requires) Provides {
	enabled := reqs.Config.GetBool("remote_agent_registry.enabled")
	if !enabled {
		return Provides{}
	}

	ra := newRemoteAgent(reqs)

	return Provides{
		Comp:          ra,
		FlareProvider: flaretypes.NewProvider(ra.fillFlare),
		Status:        status.NewInformationProvider(remoteagentregistryStatus.GetProvider(ra)),
	}
}

func newRemoteAgent(reqs Requires) *remoteAgentRegistry {
	shutdownChan := make(chan struct{})
	comp := &remoteAgentRegistry{
		conf:           reqs.Config,
		agentMap:       make(map[string]*remoteAgentDetails),
		shutdownChan:   shutdownChan,
		telemetry:      reqs.Telemetry,
		telemetryStore: newTelemetryStore(reqs.Telemetry),
	}

	reqs.Lifecycle.Append(compdef.Hook{
		OnStart: func(context.Context) error {
			go comp.start()
			return nil
		},
		OnStop: func(context.Context) error {
			shutdownChan <- struct{}{}
			return nil
		},
	})

	return comp
}

type telemetryStore struct {
	// remoteAgentRegistered tracks how many remote agents are registered.
	remoteAgentRegistered telemetry.Gauge
	// remoteAgentRegisteredError tracks how many remote agents failed to register.
	remoteAgentRegisteredError telemetry.Counter
	// remoteAgentUpdated tracks how many remote agents are updated.
	remoteAgentUpdated telemetry.Counter
	// remoteAgentUpdatedError tracks how many remote agents failed to update.
	remoteAgentUpdatedError telemetry.Counter
	// remoteAgentActionError tracks the number of errors encountered while performing actions on the remote agent registry.
	remoteAgentActionError telemetry.Counter
	// remoteAgentActionDuration tracks the duration of actions performed on the remote agent registry.
	remoteAgentActionDuration telemetry.Histogram
	// remoteAgentActionTimeout tracks the number of times an action on the remote agent registry timed out.
	remoteAgentActionTimeout telemetry.Counter
}

const (
	internalTelemetryNamespace = "remote_agent_registry"
	statusAction               = "status"
	flareAction                = "flare"
	telemetryAction            = "telemetry"
)

func newTelemetryStore(telemetryComp telemetry.Component) *telemetryStore {
	return &telemetryStore{
		remoteAgentRegistered: telemetryComp.NewGaugeWithOpts(
			internalTelemetryNamespace,
			"registered",
			[]string{"name"},
			"Number of remote agents registered in the remote agent registry.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentRegisteredError: telemetryComp.NewCounterWithOpts(
			internalTelemetryNamespace,
			"registered_error",
			[]string{"name"},
			"Number of remote agents that failed to register in the remote agent registry.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentUpdated: telemetryComp.NewCounterWithOpts(
			internalTelemetryNamespace,
			"updated",
			[]string{"name"},
			"Number of remote agents updated in the remote agent registry.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentUpdatedError: telemetryComp.NewCounterWithOpts(
			internalTelemetryNamespace,
			"updated_error",
			[]string{"name"},
			"Number of remote agents that failed to update in the remote agent registry.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentActionDuration: telemetryComp.NewHistogramWithOpts(
			internalTelemetryNamespace,
			"action_duration_seconds",
			[]string{"name", "action"},
			"Duration of actions performed on the remote agent registry.",
			// The default prometheus buckets are adapted to measure response time of network services
			prometheus.DefBuckets,
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentActionError: telemetryComp.NewCounterWithOpts(
			internalTelemetryNamespace,
			"action_error",
			[]string{"name", "action", "error"},
			"Number of errors encountered while performing actions on the remote agent registry.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
		remoteAgentActionTimeout: telemetryComp.NewCounterWithOpts(
			internalTelemetryNamespace,
			"action_timeout",
			[]string{"action"},
			"Number of times an action on the remote agent registry timed out.",
			telemetry.Options{NoDoubleUnderscoreSep: true},
		),
	}
}

// remoteAgentRegistry is the main registry for remote agents. It tracks which remote agents are currently registered, when
// they were last seen, and handles collecting status and flare data from them on request.
type remoteAgentRegistry struct {
	conf           config.Component
	agentMap       map[string]*remoteAgentDetails
	agentMapMu     sync.Mutex
	shutdownChan   chan struct{}
	telemetry      telemetry.Component
	telemetryStore *telemetryStore
}

// RegisterRemoteAgent registers a remote agent with the registry.
//
// If the remote agent is not present in the registry, it is added. If a remote agent with the same ID is already
// present, the API endpoint and display name are checked: if they are the same, then the "last seen" time of the remote
// agent is updated, otherwise the remote agent is removed and replaced with the incoming one.
func (ra *remoteAgentRegistry) RegisterRemoteAgent(registration *remoteagentregistry.RegistrationData) (uint32, error) {
	recommendedRefreshInterval := uint32(ra.conf.GetDuration("remote_agent_registry.recommended_refresh_interval").Seconds())

	ra.agentMapMu.Lock()
	defer ra.agentMapMu.Unlock()

	// If the remote agent is already registered, then we might be dealing with an update (i.e., periodic check-in) or a
	// brand new remote agent. As the agent ID may very well not be a unique ID every single time (it could always just
	// be `process-agent` or what have you), we differentiate between the two scenarios by checking the human name and
	// API endpoint give to us.
	//
	// If either of them are different, then we remove the old remote agent and add the new one. If they're the same,
	// then we just update the last seen time and move on.
	agentID := util.SanitizeAgentID(registration.AgentID)
	entry, ok := ra.agentMap[agentID]
	if !ok {
		// We've got a brand new remote agent, so add them to the registry.
		//
		// This won't try and connect to the given gRPC endpoint immediately, but will instead surface any errors with
		// connecting when we try to query the remote agent for status or flare data.
		details, err := newRemoteAgentDetails(registration)
		if err != nil {
			ra.telemetryStore.remoteAgentRegisteredError.Inc(details.sanatizedDisplayName)
			return 0, err
		}

		log.Infof("Remote agent '%s' registered.", agentID)
		ra.agentMap[agentID] = details
		ra.telemetryStore.remoteAgentRegistered.Inc(details.sanatizedDisplayName)

		return recommendedRefreshInterval, nil
	}

	// We already have an entry for this remote agent, so check if we need to update the gRPC client, and then update
	// the other bits.
	if entry.apiEndpoint != registration.APIEndpoint {
		entry.apiEndpoint = registration.APIEndpoint

		client, err := newRemoteAgentClient(registration)
		if err != nil {
			ra.telemetryStore.remoteAgentUpdatedError.Inc(sanatizeString(registration.DisplayName))
			return 0, err
		}
		entry.client = client
	}

	entry.displayName = registration.DisplayName
	entry.sanatizedDisplayName = sanatizeString(registration.DisplayName)
	entry.lastSeen = time.Now()

	ra.telemetryStore.remoteAgentUpdated.Inc(entry.sanatizedDisplayName)

	return recommendedRefreshInterval, nil
}

func (ra *remoteAgentRegistry) registerCollector() {
	ra.telemetry.RegisterCollector(newRegistryCollector(&ra.agentMapMu, ra.agentMap, ra.getQueryTimeout(), ra.telemetryStore))
}

// Start starts the remote agent registry, which periodically checks for idle remote agents and deregisters them.
func (ra *remoteAgentRegistry) start() {
	remoteAgentIdleTimeout := ra.conf.GetDuration("remote_agent_registry.idle_timeout")
	ra.registerCollector()

	go func() {
		log.Info("Remote Agent registry started.")

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ra.shutdownChan:
				log.Info("Remote Agent registry stopped.")
				return
			case <-ticker.C:
				ra.agentMapMu.Lock()

				agentsToRemove := make([]string, 0)
				for id, details := range ra.agentMap {
					if time.Since(details.lastSeen) > remoteAgentIdleTimeout {
						agentsToRemove = append(agentsToRemove, id)
					}
				}

				for _, id := range agentsToRemove {
					details, ok := ra.agentMap[id]
					if ok {
						delete(ra.agentMap, id)
						log.Infof("Remote agent '%s' deregistered after being idle for %s.", id, remoteAgentIdleTimeout)
						ra.telemetryStore.remoteAgentRegistered.Dec(details.sanatizedDisplayName)
					}
				}

				ra.agentMapMu.Unlock()
			}
		}
	}()
}

type registryCollector struct {
	agentMapMu     *sync.Mutex
	agentMap       map[string]*remoteAgentDetails
	queryTimeout   time.Duration
	telemetryStore *telemetryStore
}

func newRegistryCollector(agentMapMu *sync.Mutex, agentMap map[string]*remoteAgentDetails, queryTimeout time.Duration, telemetryStore *telemetryStore) prometheus.Collector {
	return &registryCollector{
		agentMapMu:     agentMapMu,
		agentMap:       agentMap,
		queryTimeout:   queryTimeout,
		telemetryStore: telemetryStore,
	}
}

func (c *registryCollector) Describe(_ chan<- *prometheus.Desc) {
}

func (c *registryCollector) Collect(ch chan<- prometheus.Metric) {
	c.getRegisteredAgentsTelemetry(ch)
}

func (c *registryCollector) getRegisteredAgentsTelemetry(ch chan<- prometheus.Metric) {
	c.agentMapMu.Lock()

	agentsLen := len(c.agentMap)

	// Return early if we have no registered remote agents.
	if agentsLen == 0 {
		c.agentMapMu.Unlock()
		return
	}

	data := make(chan *pb.GetTelemetryResponse, agentsLen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for agentID, details := range c.agentMap {
		go func() {
			start := time.Now()
			defer func() {
				c.telemetryStore.remoteAgentActionDuration.Observe(
					time.Since(start).Seconds(),
					details.sanatizedDisplayName,
					telemetryAction,
				)
			}()
			resp, err := details.client.GetTelemetry(ctx, &pb.GetTelemetryRequest{}, grpc.WaitForReady(true))
			if err != nil {
				log.Warnf("Failed to query remote agent '%s' for telemetry data: %v", agentID, err)
				c.telemetryStore.remoteAgentActionError.Inc(details.sanatizedDisplayName, telemetryAction, grpcErrorMessage(err))
				return
			}
			if promText, ok := resp.Payload.(*pb.GetTelemetryResponse_PromText); ok {
				collectFromPromText(ch, promText.PromText)
			}
			data <- resp
		}()
	}

	c.agentMapMu.Unlock()

	timeout := time.After(c.queryTimeout)
	responsesRemaining := agentsLen

collect:
	for {
		select {
		case <-data:
			responsesRemaining--
		case <-timeout:
			c.telemetryStore.remoteAgentActionTimeout.Inc(telemetryAction)
			break collect
		default:
			if responsesRemaining == 0 {
				break collect
			}
		}
	}

}

// Retrieve the telemetry data in exposition format from the remote agent
func collectFromPromText(ch chan<- prometheus.Metric, promText string) {
	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(strings.NewReader(promText))
	if err != nil {
		log.Warnf("Failed to parse prometheus text: %v", err)
		return
	}
	for _, mf := range metricFamilies {
		help := ""
		if mf.Help != nil {
			help = *mf.Help
		}
		for _, metric := range mf.Metric {
			labelNames := make([]string, 0, len(metric.Label))
			labelValues := make([]string, 0, len(metric.Label))
			for _, label := range metric.Label {
				labelNames = append(labelNames, *label.Name)
				labelValues = append(labelValues, *label.Value)
			}
			switch *mf.Type {
			case dto.MetricType_COUNTER:
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(*mf.Name, help, labelNames, nil),
					prometheus.CounterValue,
					*metric.Counter.Value,
					labelValues...,
				)
			case dto.MetricType_GAUGE:
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(*mf.Name, help, labelNames, nil),
					prometheus.GaugeValue,
					*metric.Gauge.Value,
					labelValues...,
				)
			}
		}
	}
}

func (ra *remoteAgentRegistry) getQueryTimeout() time.Duration {
	return ra.conf.GetDuration("remote_agent_registry.query_timeout")
}

// GetRegisteredAgents returns the list of registered remote agents.
func (ra *remoteAgentRegistry) GetRegisteredAgents() []*remoteagentregistry.RegisteredAgent {
	ra.agentMapMu.Lock()
	defer ra.agentMapMu.Unlock()

	agents := make([]*remoteagentregistry.RegisteredAgent, 0, len(ra.agentMap))
	for _, details := range ra.agentMap {
		agents = append(agents, &remoteagentregistry.RegisteredAgent{
			DisplayName:          details.displayName,
			SanatizedDisplayName: details.sanatizedDisplayName,
			LastSeenUnix:         details.lastSeen.Unix(),
		})
	}

	return agents
}

// GetRegisteredAgentStatuses returns the status of all registered remote agents.
func (ra *remoteAgentRegistry) GetRegisteredAgentStatuses() []*remoteagentregistry.StatusData {
	queryTimeout := ra.getQueryTimeout()

	ra.agentMapMu.Lock()

	agentsLen := len(ra.agentMap)
	statusMap := make(map[string]*remoteagentregistry.StatusData, agentsLen)
	agentStatuses := make([]*remoteagentregistry.StatusData, 0, agentsLen)

	// Return early if we have no registered remote agents.
	if agentsLen == 0 {
		ra.agentMapMu.Unlock()
		return agentStatuses
	}

	// We preload the status map with a response that indicates timeout, since we want to ensure there's an entry for
	// every registered remote agent even if we don't get a response back (whether good or bad) from them.
	for agentID, details := range ra.agentMap {
		statusMap[agentID] = &remoteagentregistry.StatusData{
			AgentID:       agentID,
			DisplayName:   details.displayName,
			FailureReason: fmt.Sprintf("Timed out after waiting %s for response.", queryTimeout),
		}
	}

	data := make(chan *remoteagentregistry.StatusData, agentsLen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for agentID, details := range ra.agentMap {
		go func() {
			start := time.Now()
			defer func() {
				ra.telemetryStore.remoteAgentActionDuration.Observe(
					time.Since(start).Seconds(),
					details.sanatizedDisplayName,
					statusAction,
				)
			}()

			// We push any errors into "failure reason" which ends up getting shown in the status details.
			resp, err := details.client.GetStatusDetails(ctx, &pb.GetStatusDetailsRequest{}, grpc.WaitForReady(true))
			if err != nil {
				data <- &remoteagentregistry.StatusData{
					AgentID:       agentID,
					DisplayName:   details.displayName,
					FailureReason: fmt.Sprintf("Failed to query for status: %v", err),
				}
				ra.telemetryStore.remoteAgentActionError.Inc(details.sanatizedDisplayName, statusAction, grpcErrorMessage(err))
				return
			}

			data <- raproto.ProtobufToStatusData(agentID, details.displayName, resp)
		}()
	}

	ra.agentMapMu.Unlock()

	timeout := time.After(queryTimeout)
	responsesRemaining := agentsLen

collect:
	for {
		select {
		case statusData := <-data:
			statusMap[statusData.AgentID] = statusData
			responsesRemaining--
		case <-timeout:
			ra.telemetryStore.remoteAgentActionTimeout.Inc(statusAction)
			break collect
		default:
			if responsesRemaining == 0 {
				break collect
			}
		}
	}

	// Migrate the final status data from the map into our slice, for easier consumption.
	for _, statusData := range statusMap {
		agentStatuses = append(agentStatuses, statusData)
	}

	return agentStatuses
}

func (ra *remoteAgentRegistry) fillFlare(builder flarebuilder.FlareBuilder) error {
	queryTimeout := ra.getQueryTimeout()

	ra.agentMapMu.Lock()

	agentsLen := len(ra.agentMap)
	flareMap := make(map[string]*remoteagentregistry.FlareData, agentsLen)

	// Return early if we have no registered remote agents.
	if agentsLen == 0 {
		ra.agentMapMu.Unlock()
		return nil
	}

	data := make(chan *remoteagentregistry.FlareData, agentsLen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for agentID, details := range ra.agentMap {
		go func() {
			start := time.Now()
			defer func() {
				ra.telemetryStore.remoteAgentActionDuration.Observe(
					time.Since(start).Seconds(),
					details.sanatizedDisplayName,
					flareAction,
				)
			}()

			// We push any errors into "failure reason" which ends up getting shown in the status details.
			resp, err := details.client.GetFlareFiles(ctx, &pb.GetFlareFilesRequest{}, grpc.WaitForReady(true))
			if err != nil {
				ra.telemetryStore.remoteAgentActionError.Inc(details.sanatizedDisplayName, flareAction, grpcErrorMessage(err))
				log.Warnf("Failed to query remote agent '%s' for flare data: %v", agentID, err)
				data <- nil
				return
			}

			data <- raproto.ProtobufToFlareData(agentID, resp)
		}()
	}

	ra.agentMapMu.Unlock()

	timeout := time.After(queryTimeout)
	responsesRemaining := agentsLen

collect:
	for {
		select {
		case flareData := <-data:
			flareMap[flareData.AgentID] = flareData
			responsesRemaining--
		case <-timeout:
			ra.telemetryStore.remoteAgentActionTimeout.Inc(flareAction)
			break collect
		default:
			if responsesRemaining == 0 {
				break collect
			}
		}
	}

	// We've collected all the flare data we can, so now we add it to the flare builder.
	for agentID, flareData := range flareMap {
		if flareData == nil {
			continue
		}

		for fileName, fileData := range flareData.Files {
			err := builder.AddFile(fmt.Sprintf("%s/%s", agentID, util.SanitizeFileName(fileName)), fileData)
			if err != nil {
				return fmt.Errorf("failed to add file '%s' from remote agent '%s' to flare: %w", fileName, agentID, err)
			}
		}
	}

	return nil
}

func newRemoteAgentClient(registration *remoteagentregistry.RegistrationData) (pb.RemoteAgentClient, error) {
	// NOTE: we're using InsecureSkipVerify because the gRPC server only
	// persists its TLS certs in memory, and we currently have no
	// infrastructure to make them available to clients. This is NOT
	// equivalent to grpc.WithInsecure(), since that assumes a non-TLS
	// connection.
	tlsCreds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
	})

	conn, err := grpc.NewClient(registration.APIEndpoint,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithPerRPCCredentials(ddgrpc.NewBearerTokenAuth(registration.AuthToken)),
		// Set on the higher side to account for the fact that flare file data could be larger than the default 4MB limit.
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64*1024*1024)),
	)
	if err != nil {
		return nil, err
	}

	return pb.NewRemoteAgentClient(conn), nil
}

type remoteAgentDetails struct {
	lastSeen             time.Time
	displayName          string
	sanatizedDisplayName string
	apiEndpoint          string
	client               pb.RemoteAgentClient
}

func newRemoteAgentDetails(registration *remoteagentregistry.RegistrationData) (*remoteAgentDetails, error) {
	client, err := newRemoteAgentClient(registration)
	if err != nil {
		return nil, err
	}

	return &remoteAgentDetails{
		displayName:          registration.DisplayName,
		sanatizedDisplayName: sanatizeString(registration.DisplayName),
		apiEndpoint:          registration.APIEndpoint,
		client:               client,
		lastSeen:             time.Now(),
	}, nil
}

func sanatizeString(s string) string {
	result := []string{}
	for _, s := range strings.Split(s, " ") {
		result = append(result, strings.ToLower(s))
	}
	return strings.Join(result, "-")
}

func grpcErrorMessage(err error) string {
	errorString := codes.Unknown.String()
	status, ok := grpcStatus.FromError(err)
	if ok {
		errorString = status.Code().String()
	}
	return errorString
}
