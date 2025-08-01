// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package rules holds rules related files
package rules

import (
	"errors"
	"fmt"
	"strings"

	"github.com/DataDog/datadog-agent/pkg/security/secl/compiler/eval"
)

var (
	// ErrRuleWithoutID is returned when there is no ID
	ErrRuleWithoutID = errors.New("no rule ID")

	// ErrRuleWithoutExpression is returned when there is no expression
	ErrRuleWithoutExpression = errors.New("no rule expression")

	// ErrRuleIDPattern is returned when there is no expression
	ErrRuleIDPattern = errors.New("rule ID pattern error")

	// ErrRuleWithoutEvent is returned when no event type was inferred from the rule
	ErrRuleWithoutEvent = errors.New("no event in the rule definition")

	// ErrInternalIDConflict is returned when a user defined rule use an internal ID
	ErrInternalIDConflict = errors.New("internal rule ID conflict")

	// ErrEventTypeNotEnabled is returned when an event is not enabled
	ErrEventTypeNotEnabled = errors.New("event type not enabled")

	// ErrCannotMergeExpression is returned when trying to merge SECL expression
	ErrCannotMergeExpression = errors.New("cannot merge expression")

	// ErrRuleAgentVersion is returned when there is an agent version error
	ErrRuleAgentVersion = errors.New("agent version incompatible")

	// ErrRuleAgentFilter is returned when an agent rule was filtered
	ErrRuleAgentFilter = errors.New("agent rule filtered")

	// ErrMultipleEventCategories is returned when multile event categories are in the same expansion
	ErrMultipleEventCategories = errors.New("multiple event categories in the same rule expansion")

	// ErrPolicyIsEmpty is returned when a policy has no rules or macros
	ErrPolicyIsEmpty = errors.New("the policy is empty")
)

// ErrFieldTypeUnknown is returned when a field has an unknown type
type ErrFieldTypeUnknown struct {
	Field string
}

func (e *ErrFieldTypeUnknown) Error() string {
	return fmt.Sprintf("field type unknown for `%s`", e.Field)
}

// ErrValueTypeUnknown is returned when the value of a field has an unknown type
type ErrValueTypeUnknown struct {
	Field string
}

func (e *ErrValueTypeUnknown) Error() string {
	return fmt.Sprintf("value type unknown for `%s`", e.Field)
}

// ErrNoApprover is returned when no approver was found for a set of rules
type ErrNoApprover struct {
	Fields []string
}

func (e ErrNoApprover) Error() string {
	return fmt.Sprintf("no approver for fields `%s`", strings.Join(e.Fields, ", "))
}

// ErrNoEventTypeBucket is returned when no bucket could be found for an event type
type ErrNoEventTypeBucket struct {
	EventType string
}

func (e ErrNoEventTypeBucket) Error() string {
	return fmt.Sprintf("no bucket for event type `%s`", e.EventType)
}

// ErrPolicyLoad is returned on policy file error
type ErrPolicyLoad struct {
	Name    string
	Version string
	Source  string
	Err     error
}

func (e ErrPolicyLoad) Error() string {
	return fmt.Sprintf("error loading policy `%s` from source `%s`: %s", e.Name, e.Source, e.Err)
}

// ErrMacroLoad is on macro definition error
type ErrMacroLoad struct {
	Macro *PolicyMacro
	Err   error
}

func (e ErrMacroLoad) Error() string {
	return fmt.Sprintf("macro `%s` definition error: %s", e.Macro.Def.ID, e.Err)
}

// ErrRuleLoad is on rule definition error
type ErrRuleLoad struct {
	Rule *PolicyRule
	Err  error
}

func (e ErrRuleLoad) Error() string {
	return fmt.Sprintf("rule `%s` error: %s", e.Rule.Def.ID, e.Err)
}

func (e ErrRuleLoad) Unwrap() error {
	return e.Err
}

// RuleLoadErrType defines an rule error type
type RuleLoadErrType string

const (
	// AgentVersionErrType agent version incompatible
	AgentVersionErrType RuleLoadErrType = "agent_version_error"
	// AgentFilterErrType agent filter do not match
	AgentFilterErrType RuleLoadErrType = "agent_filter_error"
	// EventTypeNotEnabledErrType event type not enabled
	EventTypeNotEnabledErrType RuleLoadErrType = "event_type_disabled"
	// SyntaxErrType syntax error
	SyntaxErrType RuleLoadErrType = "syntax_error"
	// UnknownErrType undefined error
	UnknownErrType RuleLoadErrType = "error"
)

// Type return the type of the error
func (e ErrRuleLoad) Type() RuleLoadErrType {
	switch e.Err {
	case ErrRuleAgentVersion:
		return AgentVersionErrType
	case ErrRuleAgentFilter:
		return AgentVersionErrType
	case ErrEventTypeNotEnabled:
		return EventTypeNotEnabledErrType
	}

	switch e.Err.(type) {
	case *ErrFieldTypeUnknown, *ErrValueTypeUnknown, *ErrRuleSyntax, *ErrFieldNotAvailable:
		return SyntaxErrType
	}

	return UnknownErrType
}

// ErrRuleSyntax is returned when there is a syntax error
type ErrRuleSyntax struct {
	Err error
}

func (e *ErrRuleSyntax) Error() string {
	return fmt.Sprintf("syntax error `%v`", e.Err)
}

func (e *ErrRuleSyntax) Unwrap() error {
	return e.Err
}

// ErrActionFilter is on filter definition error
type ErrActionFilter struct {
	Expression string
	Err        error
}

func (e ErrActionFilter) Error() string {
	return fmt.Sprintf("filter `%s` error: %s", e.Expression, e.Err)
}

func (e ErrActionFilter) Unwrap() error {
	return e.Err
}

// ErrScopeField is return on scope field definition error
type ErrScopeField struct {
	Expression string
	Err        error
}

func (e ErrScopeField) Error() string {
	return fmt.Sprintf("scope_field `%s` error: %s", e.Expression, e.Err)
}

func (e ErrScopeField) Unwrap() error {
	return e.Err
}

// ErrFieldNotAvailable is returned when a field is not available
type ErrFieldNotAvailable struct {
	Field        eval.Field
	EventType    eval.EventType
	RestrictedTo []eval.EventType
}

func (e *ErrFieldNotAvailable) Error() string {
	return fmt.Sprintf("field `%s` not available for event type `%v`, available for `%v`", e.Field, e.EventType, e.RestrictedTo)
}
