// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	// component dependencies
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DataDog/datadog-agent/comp/aggregator/demultiplexer"
	"github.com/DataDog/datadog-agent/comp/aggregator/demultiplexer/demultiplexerimpl"
	"github.com/DataDog/datadog-agent/comp/api/api"
	"github.com/DataDog/datadog-agent/comp/collector/collector"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/autodiscoveryimpl"
	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/flare/flareimpl"
	"github.com/DataDog/datadog-agent/comp/core/gui"
	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameinterface"
	"github.com/DataDog/datadog-agent/comp/core/log/logimpl"
	"github.com/DataDog/datadog-agent/comp/core/secrets"
	"github.com/DataDog/datadog-agent/comp/core/secrets/secretsimpl"
	"github.com/DataDog/datadog-agent/comp/core/settings"
	"github.com/DataDog/datadog-agent/comp/core/settings/settingsimpl"
	"github.com/DataDog/datadog-agent/comp/core/status"
	"github.com/DataDog/datadog-agent/comp/core/status/statusimpl"
	"github.com/DataDog/datadog-agent/comp/core/workloadmeta"
	dogstatsdServer "github.com/DataDog/datadog-agent/comp/dogstatsd/server"
	dogstatsddebug "github.com/DataDog/datadog-agent/comp/dogstatsd/serverDebug"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/serverDebug/serverdebugimpl"
	"github.com/DataDog/datadog-agent/comp/forwarder/eventplatformreceiver"
	"github.com/DataDog/datadog-agent/comp/forwarder/eventplatformreceiver/eventplatformreceiverimpl"
	logsAgent "github.com/DataDog/datadog-agent/comp/logs/agent"
	"github.com/DataDog/datadog-agent/comp/metadata/host"
	"github.com/DataDog/datadog-agent/comp/metadata/host/hostimpl"
	"github.com/DataDog/datadog-agent/comp/metadata/inventoryagent"
	"github.com/DataDog/datadog-agent/comp/metadata/inventoryagent/inventoryagentimpl"
	"github.com/DataDog/datadog-agent/comp/metadata/inventorychecks"
	"github.com/DataDog/datadog-agent/comp/metadata/inventorychecks/inventorychecksimpl"
	"github.com/DataDog/datadog-agent/comp/metadata/inventoryhost"
	"github.com/DataDog/datadog-agent/comp/metadata/inventoryhost/inventoryhostimpl"
	"github.com/DataDog/datadog-agent/comp/metadata/packagesigning"
	"github.com/DataDog/datadog-agent/comp/metadata/packagesigning/packagesigningimpl"

	// package dependencies
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/optional"

	// third-party dependencies
	"github.com/gorilla/mux"
	"go.uber.org/fx"
)

type handlerdeps struct {
	fx.In

	Server                dogstatsdServer.Component
	ServerDebug           dogstatsddebug.Component
	Wmeta                 workloadmeta.Component
	LogsAgent             optional.Option[logsAgent.Component]
	HostMetadata          host.Component
	InvAgent              inventoryagent.Component
	Demux                 demultiplexer.Component
	InvHost               inventoryhost.Component
	SecretResolver        secrets.Component
	InvChecks             inventorychecks.Component
	PkgSigning            packagesigning.Component
	StatusComponent       status.Mock
	Collector             optional.Option[collector.Component]
	EventPlatformReceiver eventplatformreceiver.Component
	Ac                    autodiscovery.Mock
	Gui                   optional.Option[gui.Component]
	Settings              settings.Component
	EndpointProviders     []api.EndpointProvider `group:"agent_endpoint"`
}

func getComponentDeps(t *testing.T) handlerdeps {
	return fxutil.Test[handlerdeps](
		t,
		logimpl.MockModule(),
		config.MockModule(),
		fx.Supply(workloadmeta.NewParams()),
		fx.Supply(context.Background()),
		workloadmeta.MockModule(),
		hostnameinterface.MockModule(),
		flareimpl.MockModule(),
		dogstatsdServer.MockModule(),
		serverdebugimpl.MockModule(),
		fx.Provide(func() optional.Option[logsAgent.Component] {
			return optional.NewNoneOption[logsAgent.Component]()
		}),
		hostimpl.MockModule(),
		inventoryagentimpl.MockModule(),
		demultiplexerimpl.MockModule(),
		inventoryhostimpl.MockModule(),
		secretsimpl.MockModule(),
		fx.Provide(func(secretMock secrets.Mock) secrets.Component {
			component := secretMock.(secrets.Component)
			return component
		}),
		inventorychecksimpl.MockModule(),
		packagesigningimpl.MockModule(),
		statusimpl.MockModule(),
		fx.Provide(func() optional.Option[collector.Component] {
			return optional.NewNoneOption[collector.Component]()
		}),
		eventplatformreceiverimpl.MockModule(),
		fx.Options(
			fx.Supply(autodiscoveryimpl.MockParams{Scheduler: nil}),
			autodiscoveryimpl.MockModule(),
		),
		fx.Supply(optional.NewNoneOption[gui.Component]()),
		settingsimpl.MockModule(),
	)
}

func setupRoutes(t *testing.T) *mux.Router {
	deps := getComponentDeps(t)
	sender := aggregator.NewNoOpSenderManager()

	router := mux.NewRouter()
	SetupHandlers(
		router,
		deps.Server,
		deps.ServerDebug,
		deps.Wmeta,
		deps.LogsAgent,
		sender,
		deps.HostMetadata,
		deps.InvAgent,
		deps.Demux,
		deps.InvHost,
		deps.SecretResolver,
		deps.InvChecks,
		deps.PkgSigning,
		deps.StatusComponent,
		deps.Collector,
		deps.EventPlatformReceiver,
		deps.Ac,
		deps.Gui,
		deps.Settings,
		deps.EndpointProviders,
	)

	return router
}

func TestSetupHandlers(t *testing.T) {
	testcases := []struct {
		route    string
		method   string
		wantCode int
	}{
		{
			route:    "/version",
			method:   "GET",
			wantCode: 200,
		},
		{
			route:    "/flare",
			method:   "POST",
			wantCode: 200,
		},
	}
	router := setupRoutes(t)
	ts := httptest.NewServer(router)
	defer ts.Close()

	for _, tc := range testcases {
		req, err := http.NewRequest(tc.method, ts.URL+tc.route, nil)
		require.NoError(t, err)

		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		assert.Equal(t, tc.wantCode, resp.StatusCode)
	}
}
