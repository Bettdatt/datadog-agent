// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package main is a go application which use dd-trace-go, in order to test
// static APM instrumentation detection. This program is never executed.
package main

import (
	"fmt"
	"time"

	"github.com/DataDog/dd-trace-go/v2/ddtrace/tracer"
)

func main() {
	err := tracer.Start()
	if err != nil {
		fmt.Println(err)
	}

	time.Sleep(time.Second * 20)
}