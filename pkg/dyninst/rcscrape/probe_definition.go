// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

package rcscrape

import (
	"math"

	"github.com/DataDog/datadog-agent/pkg/dyninst/ir"
)

// remoteConfigProbeDefinition is the probe definition for the remote config
// probe.
//
// This cooperates with dd-trace-go via an explicit function to which it
// periodically passes each individual probe.
//
// See https://github.com/DataDog/dd-trace-go/blob/4f1af406/ddtrace/tracer/remote_config.go#L238-L242

type probeDefinitionV1 struct{ probeDefinition }

func (r probeDefinitionV1) GetID() string      { return rcProbeIDV1 }
func (r probeDefinitionV1) GetWhere() ir.Where { return probeWhereV1{} }

type probeDefinitionV2 struct{ probeDefinition }

func (r probeDefinitionV2) GetID() string      { return rcProbeIDV2 }
func (r probeDefinitionV2) GetWhere() ir.Where { return probeWhereV2{} }

type symdbProbeDefinition struct{ probeDefinition }

func (r symdbProbeDefinition) GetID() string      { return rcProbeIDSymdb }
func (r symdbProbeDefinition) GetWhere() ir.Where { return probeWhereSymdb{} }

type probeDefinition struct{}

func (r probeDefinition) GetKind() ir.ProbeKind { return ir.ProbeKindSnapshot }
func (r probeDefinition) GetVersion() int       { return 0 }
func (r probeDefinition) GetCaptureConfig() ir.CaptureConfig {
	return probeCaptureConfig{}
}
func (r probeDefinition) GetTags() []string { return nil }
func (r probeDefinition) GetThrottleConfig() ir.ThrottleConfig {
	return probeThrottleConfig{}
}

type probeCaptureConfig struct{}

func (r probeCaptureConfig) GetMaxCollectionSize() uint32 { return 0 }
func (r probeCaptureConfig) GetMaxFieldCount() uint32     { return 0 }
func (r probeCaptureConfig) GetMaxReferenceDepth() uint32 { return 3 }

const rcProbeIDV1 = "remote-config-v1"
const rcProbeIDV2 = "remote-config-v2"
const rcProbeIDSymdb = "remote-config-v2-symdb"

// probeThrottleConfig is the throttle configuration for the remote
// config probe. It is set to a very high value to ensure that we do not miss
// any probes.
type probeThrottleConfig struct{}

func (r probeThrottleConfig) GetThrottleBudget() int64    { return math.MaxInt64 }
func (r probeThrottleConfig) GetThrottlePeriodMs() uint32 { return 100 }

type probeWhereV1 struct{}

var _ ir.FunctionWhere = probeWhereV1{}

const v1PassProbeConfiguration = "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer.passProbeConfiguration"

func (p probeWhereV1) Where() {}
func (p probeWhereV1) Location() string {
	return v1PassProbeConfiguration
}

type probeWhereV2 struct{}

var _ ir.FunctionWhere = probeWhereV2{}

const v2PassProbeConfiguration = "github.com/DataDog/dd-trace-go/v2/ddtrace/tracer.passProbeConfiguration"

func (p probeWhereV2) Where() {}
func (p probeWhereV2) Location() string {
	return v2PassProbeConfiguration
}

const symdbPassProbeConfiguration = "github.com/DataDog/dd-trace-go/v2/ddtrace/tracer.passSymDBState"

type probeWhereSymdb struct{}

var _ ir.FunctionWhere = probeWhereSymdb{}

func (p probeWhereSymdb) Where() {}
func (p probeWhereSymdb) Location() string {
	return symdbPassProbeConfiguration
}
