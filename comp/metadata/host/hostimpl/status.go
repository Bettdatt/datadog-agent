// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package hostimpl implements a component to generate the 'host' metadata payload (also known as "v5").
package hostimpl

import (
	"context"
	"embed"
	"encoding/json"
	"expvar"
	"io"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/hostname/hostnameinterface"
	"github.com/DataDog/datadog-agent/comp/core/status"
	"github.com/DataDog/datadog-agent/comp/metadata/host/hostimpl/utils"
)

//go:embed status_templates
var templatesFS embed.FS

// StatusProvider implements the status provider interface
type StatusProvider struct {
	Config   config.Component
	Hostname hostnameinterface.Component
}

// Name returns the name
func (p StatusProvider) Name() string {
	return "Hostname"
}

// Index returns the index
func (p StatusProvider) Index() int {
	return 1
}

func (p StatusProvider) getStatusInfo() map[string]interface{} {
	stats := make(map[string]interface{})

	p.populateStatus(stats)

	return stats
}

func (p StatusProvider) populateStatus(stats map[string]interface{}) {
	hostnameStatsJSON := []byte(expvar.Get("hostname").String())
	hostnameStats := make(map[string]interface{})
	json.Unmarshal(hostnameStatsJSON, &hostnameStats) //nolint:errcheck
	stats["hostnameStats"] = hostnameStats

	payload := utils.GetFromCache(context.TODO(), p.Config, p.Hostname)
	metadataStats := make(map[string]interface{})
	payloadBytes, _ := json.Marshal(payload)

	json.Unmarshal(payloadBytes, &metadataStats) //nolint:errcheck

	stats["metadata"] = metadataStats

	hostTags := make([]string, 0, len(payload.HostTags.System)+len(payload.HostTags.GoogleCloudPlatform))
	hostTags = append(hostTags, payload.HostTags.System...)
	hostTags = append(hostTags, payload.HostTags.GoogleCloudPlatform...)
	stats["hostTags"] = hostTags
	hostinfo := utils.GetInformation()
	hostinfoMap := make(map[string]interface{})
	hostinfoBytes, _ := json.Marshal(hostinfo)
	json.Unmarshal(hostinfoBytes, &hostinfoMap) //nolint:errcheck
	stats["hostinfo"] = hostinfoMap
}

// JSON populates the status map
func (p StatusProvider) JSON(_ bool, stats map[string]interface{}) error {
	p.populateStatus(stats)

	return nil
}

// Text renders the text output
func (p StatusProvider) Text(_ bool, buffer io.Writer) error {
	return status.RenderText(templatesFS, "host.tmpl", buffer, p.getStatusInfo())
}

// HTML renders the html output
func (p StatusProvider) HTML(_ bool, buffer io.Writer) error {
	return status.RenderHTML(templatesFS, "hostHTML.tmpl", buffer, p.getStatusInfo())
}
