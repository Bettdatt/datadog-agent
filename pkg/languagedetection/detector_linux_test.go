// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

package languagedetection

import (
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/languagedetection/languagemodels"
	languagepb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/languagedetection"
	"github.com/DataDog/datadog-agent/pkg/system-probe/api/server"
)

func startTestUnixServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	socketPath := path.Join(t.TempDir(), "test.sock")
	listener, err := server.NewListener(socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)

	return socketPath
}

func TestBinaryAnalysisClient(t *testing.T) {
	socketPath := startTestUnixServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		b, err := proto.Marshal(&languagepb.DetectLanguageResponse{
			Languages: []*languagepb.Language{
				{
					Name:    string(languagemodels.Go),
					Version: "1.19",
				},
				{
					Name:    string(languagemodels.Go),
					Version: "1.20",
				},
				{
					Name:    string(languagemodels.Go),
					Version: "1.13",
				},
			},
		})
		require.NoError(t, err)

		_, err = writer.Write(b)
		require.NoError(t, err)
	}))

	var procs []languagemodels.Process
	for _, command := range [][]string{
		{"python3", "--version"},
		{"go", "run", "main.go"},
		{"java", "-c", "org.elasticsearch.bootstrap.Elasticsearch"},
		{"process-agent"},
		{"my-internal-go-service", "-p", "8080"},
		{"xonotic"},
	} {
		procs = append(procs, makeProcess(command, command[0]))
	}

	cfg := configmock.New(t)
	cfg.SetWithoutSource("system_probe_config.language_detection.enabled", true)
	cfg.SetWithoutSource("system_probe_config.sysprobe_socket", socketPath)

	res := DetectLanguage(procs, cfg)
	assert.Equal(t, languagemodels.Python, res[0].Name)

	assert.Equal(t, languagemodels.Go, res[1].Name)
	assert.Equal(t, "1.19", res[1].Version)

	assert.Equal(t, languagemodels.Java, res[2].Name)

	assert.Equal(t, languagemodels.Go, res[3].Name)
	assert.Equal(t, "1.20", res[3].Version)

	assert.Equal(t, languagemodels.Go, res[4].Name)
	assert.Equal(t, "1.13", res[4].Version)

	assert.Equal(t, languagemodels.Unknown, res[5].Name)
}
