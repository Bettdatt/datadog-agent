// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build tools

// we won't really ever use the tools build tag we use it to ignore this file
// but track the dependencies in go.mod

package apiimpl

import (
	_ "github.com/golang/protobuf/protoc-gen-go"
)
