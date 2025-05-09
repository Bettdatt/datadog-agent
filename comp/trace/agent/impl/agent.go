// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package agentimpl defines the tracer agent.
package agentimpl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"sync"
	"syscall"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/fx"

	ipc "github.com/DataDog/datadog-agent/comp/core/ipc/def"
	"github.com/DataDog/datadog-agent/comp/core/secrets"
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/statsd"
	traceagent "github.com/DataDog/datadog-agent/comp/trace/agent/def"
	compression "github.com/DataDog/datadog-agent/comp/trace/compression/def"
	"github.com/DataDog/datadog-agent/comp/trace/config"
	"github.com/DataDog/datadog-agent/pkg/config/env"
	"github.com/DataDog/datadog-agent/pkg/pidfile"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/trace"
	agentrt "github.com/DataDog/datadog-agent/pkg/runtime"
	pkgagent "github.com/DataDog/datadog-agent/pkg/trace/agent"
	tracecfg "github.com/DataDog/datadog-agent/pkg/trace/config"
	"github.com/DataDog/datadog-agent/pkg/trace/telemetry"
	"github.com/DataDog/datadog-agent/pkg/trace/watchdog"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/option"
	"github.com/DataDog/datadog-agent/pkg/version"

	ddgostatsd "github.com/DataDog/datadog-go/v5/statsd"
	"github.com/DataDog/opentelemetry-mapping-go/pkg/otlp/attributes"
	"github.com/DataDog/opentelemetry-mapping-go/pkg/otlp/attributes/source"
)

const messageAgentDisabled = `trace-agent not enabled. Set the environment variable
DD_APM_ENABLED=true or add "apm_config.enabled: true" entry
to your datadog.yaml. Exiting...`

// ErrAgentDisabled indicates that the trace-agent wasn't enabled through environment variable or config.
var ErrAgentDisabled = errors.New(messageAgentDisabled)

type dependencies struct {
	fx.In

	Lc         fx.Lifecycle
	Shutdowner fx.Shutdowner

	Config             config.Component
	Secrets            option.Option[secrets.Component]
	Context            context.Context
	Params             *Params
	TelemetryCollector telemetry.TelemetryCollector
	Statsd             statsd.Component
	Tagger             tagger.Component
	Compressor         compression.Component
	IPC                ipc.Component
}

var _ traceagent.Component = (*component)(nil)

func (c component) SetOTelAttributeTranslator(attrstrans *attributes.Translator) {
	c.Agent.OTLPReceiver.SetOTelAttributeTranslator(attrstrans)
}

func (c component) ReceiveOTLPSpans(ctx context.Context, rspans ptrace.ResourceSpans, httpHeader http.Header, hostFromAttributesHandler attributes.HostFromAttributesHandler) source.Source {
	return c.Agent.OTLPReceiver.ReceiveResourceSpans(ctx, rspans, httpHeader, hostFromAttributesHandler)
}

func (c component) SendStatsPayload(p *pb.StatsPayload) {
	c.Agent.StatsWriter.SendPayload(p)
}

func (c component) GetHTTPHandler(endpoint string) http.Handler {
	c.Agent.Receiver.BuildHandlers()
	if v, ok := c.Agent.Receiver.Handlers[endpoint]; ok {
		return v
	}
	return nil
}

type component struct {
	*pkgagent.Agent

	cancel             context.CancelFunc
	config             config.Component
	secrets            option.Option[secrets.Component]
	params             *Params
	tagger             tagger.Component
	telemetryCollector telemetry.TelemetryCollector
	ipc                ipc.Component
	wg                 *sync.WaitGroup
}

// NewAgent creates a new Agent component.
func NewAgent(deps dependencies) (traceagent.Component, error) {
	c := component{}
	tracecfg := deps.Config.Object()
	if !tracecfg.Enabled {
		log.Info(messageAgentDisabled)
		deps.TelemetryCollector.SendStartupError(telemetry.TraceAgentNotEnabled, fmt.Errorf(""))
		// Required to signal that the whole app must stop.
		_ = deps.Shutdowner.Shutdown()
		return c, nil
	}
	ctx, cancel := context.WithCancel(deps.Context) // Several related non-components require a shared context to gracefully stop.
	c = component{
		cancel:             cancel,
		config:             deps.Config,
		secrets:            deps.Secrets,
		params:             deps.Params,
		telemetryCollector: deps.TelemetryCollector,
		tagger:             deps.Tagger,
		ipc:                deps.IPC,
		wg:                 &sync.WaitGroup{},
	}
	statsdCl, err := setupMetrics(deps.Statsd, c.config, c.telemetryCollector)
	if err != nil {
		return nil, err
	}
	setupShutdown(ctx, deps.Shutdowner, statsdCl)

	prepGoRuntime(tracecfg)

	c.Agent = pkgagent.NewAgent(
		ctx,
		c.config.Object(),
		c.telemetryCollector,
		statsdCl,
		deps.Compressor,
	)

	c.config.OnUpdateAPIKey(c.UpdateAPIKey)

	deps.Lc.Append(fx.Hook{
		// Provided contexts have a timeout, so it can't be used for gracefully stopping long-running components.
		// These contexts are cancelled on a deadline, so they would have side effects on the agent.
		OnStart: func(_ context.Context) error { return start(c) },
		OnStop:  func(_ context.Context) error { return stop(c) },
	})
	return c, nil
}

func prepGoRuntime(tracecfg *tracecfg.AgentConfig) {
	cgsetprocs := agentrt.SetMaxProcs()
	if !cgsetprocs {
		if mp, ok := os.LookupEnv("GOMAXPROCS"); ok {
			log.Infof("GOMAXPROCS manually set to %v", mp)
		} else if tracecfg.MaxCPU > 0 {
			allowedCores := max(int(tracecfg.MaxCPU), 1)
			if allowedCores < runtime.GOMAXPROCS(0) {
				log.Infof("apm_config.max_cpu is less than current GOMAXPROCS. Setting GOMAXPROCS to (%v) %d\n", allowedCores, allowedCores)
				runtime.GOMAXPROCS(int(allowedCores))
			}
		} else {
			log.Infof("apm_config.max_cpu is disabled. leaving GOMAXPROCS at current value.")
		}
	}
	log.Infof("Trace Agent final GOMAXPROCS: %v", runtime.GOMAXPROCS(0))

	cgmem, err := agentrt.SetGoMemLimit(env.IsContainerized())
	if err != nil {
		log.Infof("Couldn't set Go memory limit from cgroup: %s", err)
	}
	if cgmem == 0 {
		// memory limit not set from cgroups
		if lim, ok := os.LookupEnv("GOMEMLIMIT"); ok {
			log.Infof("GOMEMLIMIT manually set to: %v", lim)
		} else if tracecfg.MaxMemory > 0 {
			// We have apm_config.max_memory, and no cgroup memory limit is in place.
			// log.Infof("apm_config.max_memory: %vMiB", int64(tracecfg.MaxMemory)/(1024*1024))
			finalmem := int64(tracecfg.MaxMemory * 0.9)
			debug.SetMemoryLimit(finalmem)
			log.Infof("apm_config.max_memory set to: %vMiB. Setting GOMEMLIMIT to 90%% of max: %vMiB", int64(tracecfg.MaxMemory)/(1024*1024), finalmem/(1024*1024))
		} else {
			// There are no memory constraints
			log.Infof("GOMEMLIMIT unconstrained.")
		}
	} else {
		log.Infof("Memory constrained by cgroup. GOMEMLIMIT is: %vMiB", cgmem/(1024*1024))
	}
}

func start(ag component) error {
	if ag.params.CPUProfile != "" {
		f, err := os.Create(ag.params.CPUProfile)
		if err != nil {
			log.Error(err)
		}
		pprof.StartCPUProfile(f) //nolint:errcheck
		log.Info("CPU profiling started...")
	}
	if ag.params.PIDFilePath != "" {
		err := pidfile.WritePID(ag.params.PIDFilePath)
		if err != nil {
			ag.telemetryCollector.SendStartupError(telemetry.CantWritePIDFile, err)
			log.Criticalf("Error writing PID file, exiting: %v", err)
			os.Exit(1)
		}

		log.Infof("PID '%d' written to PID file '%s'", os.Getpid(), ag.params.PIDFilePath)
	}

	if err := runAgentSidekicks(ag); err != nil {
		return err
	}
	ag.wg.Add(1)
	go func() {
		defer ag.wg.Done()
		ag.Run()
	}()
	return nil
}

func setupMetrics(statsd statsd.Component, cfg config.Component, telemetryCollector telemetry.TelemetryCollector) (ddgostatsd.ClientInterface, error) {
	addr, err := findAddr(cfg.Object())
	if err != nil {
		return nil, err
	}

	// TODO: Try to use statsd.Get() everywhere instead in the long run.
	client, err := statsd.CreateForAddr(addr, ddgostatsd.WithTags([]string{"version:" + version.AgentVersion}))
	if err != nil {
		telemetryCollector.SendStartupError(telemetry.CantConfigureDogstatsd, err)
		return nil, fmt.Errorf("cannot configure dogstatsd: %v", err)
	}

	err = client.Count("datadog.trace_agent.started", 1, nil, 1)
	if err != nil {
		log.Error("Failed to emit datadog.trace_agent.started metric: ", err)
	}
	return client, nil
}

func stop(ag component) error {
	ag.cancel()
	ag.wg.Wait()
	if err := ag.Statsd.Flush(); err != nil {
		log.Error("Could not flush statsd: ", err)
	}
	stopAgentSidekicks(ag.config, ag.Statsd, ag.params.DisableInternalProfiling)
	if ag.params.CPUProfile != "" {
		pprof.StopCPUProfile()
	}
	if ag.params.PIDFilePath != "" {
		os.Remove(ag.params.PIDFilePath)
	}
	if ag.params.MemProfile == "" {
		return nil
	}
	// prepare to collect memory profile
	f, err := os.Create(ag.params.MemProfile)
	if err != nil {
		log.Error("Could not create memory profile: ", err)
	}
	defer f.Close()

	// get up-to-date statistics
	runtime.GC()
	// Not using WriteHeapProfile but instead calling WriteTo to
	// make sure we pass debug=1 and resolve pointers to names.
	if err := pprof.Lookup("heap").WriteTo(f, 1); err != nil {
		log.Error("Could not write memory profile: ", err)
	}
	return nil
}

// handleSignal closes a channel to exit cleanly from routines
func handleSignal(shutdowner fx.Shutdowner, statsd ddgostatsd.ClientInterface) {
	defer watchdog.LogOnPanic(statsd)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)
	for signo := range sigChan {
		switch signo {
		case syscall.SIGINT, syscall.SIGTERM:
			log.Infof("Received signal %d (%v)", signo, signo)
			_ = shutdowner.Shutdown()
			return
		case syscall.SIGPIPE:
			// By default systemd redirects the stdout to journald. When journald is stopped or crashes we receive a SIGPIPE signal.
			// Go ignores SIGPIPE signals unless it is when stdout or stdout is closed, in this case the agent is stopped.
			// We never want the agent to stop upon receiving SIGPIPE, so we intercept the SIGPIPE signals and just discard them.
		default:
			log.Warnf("Unhandled signal %d (%v)", signo, signo)
		}
	}
}

// findAddr finds the correct address to connect to the Dogstatsd server.
func findAddr(conf *tracecfg.AgentConfig) (string, error) {
	if conf.StatsdPort > 0 {
		// UDP enabled
		return net.JoinHostPort(conf.StatsdHost, strconv.Itoa(conf.StatsdPort)), nil
	}
	if conf.StatsdPipeName != "" {
		// Windows Pipes can be used
		return `\\.\pipe\` + conf.StatsdPipeName, nil
	}
	if conf.StatsdSocket != "" {
		// Unix sockets can be used
		return `unix://` + conf.StatsdSocket, nil
	}
	return "", errors.New("dogstatsd_port is set to 0 and no alternative is available")
}
