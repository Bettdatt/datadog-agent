// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2025-present Datadog, Inc.

//go:build linux

package common

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// AFPacketSource is a PacketSource implementation using AF_PACKET.
// Why not use gopacket? Mainly because gopacket doesn't have read deadlines which we rely on.
// Also, the zero-copy ringbuffer setup is unnecessary for traceroutes.
type AFPacketSource struct {
	sock *os.File
}

var _ PacketSource = &AFPacketSource{}

// ethPAllNetwork is all protocols, in network byte order
var ethPAllNetwork = htons(uint16(unix.ETH_P_ALL))

// NewAFPacketSource creates a new AFPacketSource
func NewAFPacketSource() (*AFPacketSource, error) {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK, int(ethPAllNetwork))
	if err != nil {
		return nil, fmt.Errorf("NewAFPacketSource failed to create socket: %s", err)
	}

	sock := os.NewFile(uintptr(fd), "")
	return &AFPacketSource{sock: sock}, nil
}

// SetReadDeadline sets the deadline for when a Read() call must finish
func (a *AFPacketSource) SetReadDeadline(t time.Time) error {
	return a.sock.SetReadDeadline(t)
}

// Read reads a packet (including the ethernet frame)
func (a *AFPacketSource) Read(buf []byte) (int, error) {
	return a.sock.Read(buf)
}

// Close closes the socket
func (a *AFPacketSource) Close() error {
	return a.sock.Close()
}

// htons converts a short (uint16) from host-to-network byte order.
func htons(i uint16) uint16 {
	return i<<8 | i>>8
}
