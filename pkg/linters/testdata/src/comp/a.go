// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package a is a test package to test the linter
package a

import _ "github.com/DataDog/datadog-agent/pkg/config/setup" // want `pkg/config/setup should not be used inside comp folder`
