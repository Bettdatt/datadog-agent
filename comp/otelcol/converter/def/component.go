// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package converter defines the otel agent converter component.
package converter

import (
	"go.opentelemetry.io/collector/confmap"
)

// team: opentelemetry-agent

// Component implements the confmap.Converter interface.
type Component interface {
	confmap.Converter
}
