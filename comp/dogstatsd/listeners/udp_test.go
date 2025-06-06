// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.
//go:build !windows

package listeners

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/telemetry"
	"github.com/DataDog/datadog-agent/comp/core/telemetry/telemetryimpl"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/packets"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/pidmap"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/pidmap/pidmapimpl"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
)

type listenerDeps struct {
	fx.In

	Config    config.Component
	PidMap    pidmap.Component
	Telemetry telemetry.Component
}

func fulfillDepsWithConfig(t testing.TB, overrides map[string]interface{}) listenerDeps {
	return fxutil.Test[listenerDeps](t, fx.Options(
		telemetryimpl.MockModule(),
		config.MockModule(),
		pidmapimpl.Module(),
		fx.Replace(config.MockParams{Overrides: overrides}),
	))
}

func newPacketPoolManagerUDP(cfg config.Component, packetsTelemetryStore *packets.TelemetryStore) *packets.PoolManager[packets.Packet] {
	packetPoolUDP := packets.NewPool(cfg.GetInt("dogstatsd_buffer_size"), packetsTelemetryStore)
	return packets.NewPoolManager[packets.Packet](packetPoolUDP)
}

func TestNewUDPListener(t *testing.T) {
	deps := fulfillDepsWithConfig(t, map[string]interface{}{"dogstatsd_port": "__random__"})
	telemetryStore := NewTelemetryStore(nil, deps.Telemetry)
	packetsTelemetryStore := packets.NewTelemetryStore(nil, deps.Telemetry)
	s, err := NewUDPListener(nil, newPacketPoolManagerUDP(deps.Config, packetsTelemetryStore), deps.Config, nil, telemetryStore, packetsTelemetryStore)

	assert.NotNil(t, s)
	assert.Nil(t, err)

	s.Stop()
}

func TestUDPListenerTelemetry(t *testing.T) {
	port, err := getAvailableUDPPort()
	require.Nil(t, err)
	cfg := map[string]interface{}{}
	cfg["dogstatsd_port"] = port
	cfg["dogstatsd_non_local_traffic"] = false

	packetChannel := make(chan packets.Packets)
	deps := fulfillDepsWithConfig(t, cfg)
	telemetryStore := NewTelemetryStore(nil, deps.Telemetry)
	packetsTelemetryStore := packets.NewTelemetryStore(nil, deps.Telemetry)
	s, err := NewUDPListener(packetChannel, newPacketPoolManagerUDP(deps.Config, packetsTelemetryStore), deps.Config, nil, telemetryStore, packetsTelemetryStore)
	require.NotNil(t, s)
	assert.Nil(t, err)

	mConn := defaultMConn(s.conn.LocalAddr(), []byte("hello world"))
	s.conn.Close()
	s.conn = mConn
	s.Listen()
	defer s.Stop()

	select {
	case pkts := <-packetChannel:
		packet := pkts[0]
		assert.NotNil(t, packet)

		telemetryMock, ok := deps.Telemetry.(telemetry.Mock)
		assert.True(t, ok)

		packetsMetrics, err := telemetryMock.GetCountMetric("dogstatsd", "udp_packets")
		require.NoError(t, err)
		require.Len(t, packetsMetrics, 1)
		bytesCountMetrics, err := telemetryMock.GetCountMetric("dogstatsd", "udp_packets_bytes")
		require.NoError(t, err)
		require.Len(t, bytesCountMetrics, 1)

		assert.Equal(t, float64(1), packetsMetrics[0].Value())
		assert.Equal(t, float64(11), bytesCountMetrics[0].Value())

	case <-time.After(2 * time.Second):
		assert.FailNow(t, "Timeout on receive channel")
	}
}

func TestUDPReceive(t *testing.T) {
	var contents = []byte("daemon:666|g|#sometag1:somevalue1,sometag2:somevalue2")
	port, err := getAvailableUDPPort()
	require.Nil(t, err)

	cfg := map[string]interface{}{}
	cfg["dogstatsd_port"] = port

	packetChannel := make(chan packets.Packets)
	deps := fulfillDepsWithConfig(t, cfg)
	telemetryStore := NewTelemetryStore(nil, deps.Telemetry)
	packetsTelemetryStore := packets.NewTelemetryStore(nil, deps.Telemetry)
	s, err := NewUDPListener(packetChannel, newPacketPoolManagerUDP(deps.Config, packetsTelemetryStore), deps.Config, nil, telemetryStore, packetsTelemetryStore)
	require.Nil(t, err)
	require.NotNil(t, s)
	mockConn := defaultMConn(s.conn.LocalAddr(), contents)
	s.conn.Close()
	s.conn = mockConn
	s.Listen()
	defer s.Stop()

	select {
	case pkts := <-packetChannel:
		packet := pkts[0]
		assert.NotNil(t, packet)
		assert.Equal(t, 1, len(pkts))
		assert.Equal(t, contents, packet.Contents)
		assert.Equal(t, "", packet.Origin)
		assert.Equal(t, packet.Source, packets.UDP)

		telemetryMock, ok := deps.Telemetry.(telemetry.Mock)
		assert.True(t, ok)

		packetsMetrics, err := telemetryMock.GetCountMetric("dogstatsd", "udp_packets")
		require.NoError(t, err)
		require.Len(t, packetsMetrics, 1)
		bytesCountMetrics, err := telemetryMock.GetCountMetric("dogstatsd", "udp_packets_bytes")
		require.NoError(t, err)
		require.Len(t, bytesCountMetrics, 1)
		histogramMetrics, err := telemetryMock.GetHistogramMetric("dogstatsd", "listener_read_latency")
		require.NoError(t, err)
		require.Len(t, histogramMetrics, 1)

		assert.Equal(t, float64(1), packetsMetrics[0].Value())
		assert.Equal(t, float64(len(contents)), bytesCountMetrics[0].Value())
		assert.NotEqual(t, 0, histogramMetrics[0].Value())
	case <-time.After(2 * time.Second):
		assert.FailNow(t, "Timeout on receive channel")
	}
}

// Reproducer for https://github.com/DataDog/datadog-agent/issues/6803
func TestNewUDPListenerWhenBusyWithSoRcvBufSet(t *testing.T) {
	port, err := getAvailableUDPPort()
	assert.Nil(t, err)
	address, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	conn, err := net.ListenUDP("udp", address)
	assert.NotNil(t, conn)
	assert.Nil(t, err)
	defer conn.Close()

	cfg := map[string]interface{}{}
	cfg["dogstatsd_so_rcvbuf"] = 1
	cfg["dogstatsd_port"] = port
	cfg["dogstatsd_non_local_traffic"] = false

	deps := fulfillDepsWithConfig(t, cfg)
	telemetryStore := NewTelemetryStore(nil, deps.Telemetry)
	packetsTelemetryStore := packets.NewTelemetryStore(nil, deps.Telemetry)
	s, err := NewUDPListener(nil, newPacketPoolManagerUDP(deps.Config, packetsTelemetryStore), deps.Config, nil, telemetryStore, packetsTelemetryStore)
	assert.Nil(t, s)
	assert.NotNil(t, err)
}

// getAvailableUDPPort requests a random port number and makes sure it is available
func getAvailableUDPPort() (int, error) {
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		return -1, fmt.Errorf("can't find an available udp port: %s", err)
	}
	defer conn.Close()

	_, portString, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return -1, fmt.Errorf("can't find an available udp port: %s", err)
	}
	portInt, err := strconv.Atoi(portString)
	if err != nil {
		return -1, fmt.Errorf("can't convert udp port: %s", err)
	}

	return portInt, nil
}

// getLocalIP returns the first non loopback local IPv4 on that host
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

func defaultMConn(addr net.Addr, bs ...[]byte) *udpMock {
	return &udpMock{bufferList: slices.Clone(bs), address: addr}
}

type udpMock struct {
	bufferList [][]byte
	address    net.Addr
	nextMsg    int
}

func (conn udpMock) LocalAddr() net.Addr {
	return conn.address
}

func (conn *udpMock) ReadFrom(b []byte) (int, net.Addr, error) {
	if conn.nextMsg == len(conn.bufferList) {
		return 0, conn.address, errors.New("Attempted use of closed network connection")
	}
	buffer := conn.bufferList[conn.nextMsg]
	conn.nextMsg++
	n := copy(b, buffer)

	return n, conn.address, nil
}

func (conn udpMock) Close() error {
	return nil
}
