// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package tcp

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/DataDog/datadog-agent/pkg/logs/client"
	"github.com/DataDog/datadog-agent/pkg/logs/client/mock"
	"github.com/DataDog/datadog-agent/pkg/logs/message"
	"github.com/DataDog/datadog-agent/pkg/logs/metrics"
	"github.com/DataDog/datadog-agent/pkg/logs/status/statusinterface"

	"github.com/DataDog/datadog-agent/comp/logs/agent/config"
)

func TestDestinationHA(t *testing.T) {
	variants := []bool{true, false}
	for _, variant := range variants {
		endpoint := config.Endpoint{
			IsMRF: variant,
		}
		isEndpointMRF := endpoint.IsMRF

		dest := NewDestination(endpoint, false, client.NewDestinationsContext(), false, statusinterface.NewStatusProviderMock())
		isDestMRF := dest.IsMRF()

		assert.Equal(t, isEndpointMRF, isDestMRF)
	}
}

// TestConnecitivityDiagnoseNoBlock ensures the connectivity diagnose doesn't
// block
func TestConnecitivityDiagnoseNoBlock(t *testing.T) {
	endpoint := config.NewEndpoint("00000000", "", "host", 0, config.EmptyPathPrefix, true)
	done := make(chan struct{})

	go func() {
		CheckConnectivityDiagnose(endpoint, 1)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("TCP diagnosis check blocked for too long.")
	}
}

// TestConnectivityDiagnoseFails ensures the connectivity diagnosis connects
// successfully
func TestConnectivityDiagnoseOperationSuccess(t *testing.T) {
	// Start the test TCP server
	intake := mock.NewMockLogsIntake(t)
	serverAddr := intake.Addr().String()

	// Simulate a client connecting to the server
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("Failed to connect to test TCP server: %v", err)
	}
	defer conn.Close()

	host, port, err := net.SplitHostPort(serverAddr)
	assert.Nil(t, err)
	portInt, err := strconv.Atoi(port)
	assert.Nil(t, err)

	testSuccessEndpoint := config.NewEndpoint("api-key", "", host, portInt, config.EmptyPathPrefix, false)
	connManager := NewConnectionManager(testSuccessEndpoint, statusinterface.NewNoopStatusProvider())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = connManager.NewConnection(ctx)
	assert.Nil(t, err)
}

// TestConnectivityDiagnoseOperationFail ensure the connectivity diagnosis fails
// when provided with incorrect information
func TestConnectivityDiagnoseOperationFail(t *testing.T) {
	// Start the test TCP server
	intake := mock.NewMockLogsIntake(t)
	serverAddr := intake.Addr().String()

	// Simulate a client connecting to the server
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("Failed to connect to test TCP server: %v", err)
	}
	defer conn.Close()

	host, port, err := net.SplitHostPort(serverAddr)
	assert.Nil(t, err)
	portInt, err := strconv.Atoi(port)
	assert.Nil(t, err)

	testFailEndpointWrongAddress := config.NewEndpoint("api-key", "", "failhost", portInt, config.EmptyPathPrefix, false)
	connManager := NewConnectionManager(testFailEndpointWrongAddress, statusinterface.NewNoopStatusProvider())
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = connManager.NewConnection(ctx)
	assert.NotNil(t, err)

	testFailEndpointWrongPort := config.NewEndpoint("api-key", "", host, portInt+1, config.EmptyPathPrefix, false)
	connManager = NewConnectionManager(testFailEndpointWrongPort, statusinterface.NewNoopStatusProvider())
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = connManager.NewConnection(ctx)
	assert.NotNil(t, err)
}

type mockConn struct {
	net.TCPConn
}

func (c *mockConn) Write(_ []byte) (n int, err error) {
	return 0, errors.New("write error")
}

func (c *mockConn) Close() (err error) {
	return nil
}

// TestNoRetryAndWriteError ensures that a write error successfully exits the send loop when
// retry is disabled.
func TestNoRetryAndWriteError(t *testing.T) {
	endpoint := config.NewEndpoint("api-key", "", "localhost", 0, config.EmptyPathPrefix, false)

	dest := NewDestination(endpoint, false, client.NewDestinationsContext(), false, statusinterface.NewStatusProviderMock())
	output := make(chan *message.Payload, 1)
	dest.conn = &mockConn{}

	// Connection resets prompted a panic on earlier code versions, make sure that wasn't reintroduced.
	dest.connCreationTime = time.Now().Add(-2 * time.Second)
	endpoint.ConnectionResetInterval = time.Second

	dest.sendAndRetry(message.NewPayload([]*message.MessageMetadata{}, []byte("test"), "source", 1), output, nil)
	drops := metrics.DestinationLogsDropped.Get(endpoint.Host)
	assert.Equal(t, "1", drops.String())
	assert.Empty(t, output)
	assert.Nil(t, dest.conn)
}
