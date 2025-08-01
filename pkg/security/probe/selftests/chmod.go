// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package selftests holds selftests related files
package selftests

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/DataDog/datadog-agent/pkg/security/secl/compiler/eval"
	"github.com/DataDog/datadog-agent/pkg/security/secl/rules"
)

// ChmodSelfTest defines a chmod self test
type ChmodSelfTest struct {
	ruleID    eval.RuleID
	filename  string
	isSuccess bool
}

// GetRuleDefinition returns the rule
func (o *ChmodSelfTest) GetRuleDefinition() *rules.RuleDefinition {
	o.ruleID = fmt.Sprintf("%s_chmod", ruleIDPrefix)

	return &rules.RuleDefinition{
		ID:         o.ruleID,
		Expression: fmt.Sprintf(`chmod.file.path == "%s"`, o.filename),
		Silent:     true,
	}
}

// GenerateEvent generate an event
func (o *ChmodSelfTest) GenerateEvent(ctx context.Context) error {
	o.isSuccess = false

	// we need to use chmod (or any other external program) as our PID is discarded by probes
	// so the events would not be generated
	cmd := exec.CommandContext(ctx, "chmod", "777", o.filename)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running chmod: %w", err)
	}

	return nil
}

// HandleEvent handles self test events
func (o *ChmodSelfTest) HandleEvent(event selfTestEvent) {
	o.isSuccess = event.RuleID == o.ruleID
}

// IsSuccess return the state of the test
func (o *ChmodSelfTest) IsSuccess() bool {
	return o.isSuccess
}
