// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build windows

package packages

import "context"

// SetupAPMInjector noop
func SetupAPMInjector(_ context.Context) error {
	return nil
}

// RemoveAPMInjector noop
func RemoveAPMInjector(_ context.Context) error { return nil }

// InstrumentAPMInjector noop
func InstrumentAPMInjector(_ context.Context, _ string) error { return nil }

// UninstrumentAPMInjector noop
func UninstrumentAPMInjector(_ context.Context, _ string) error { return nil }
