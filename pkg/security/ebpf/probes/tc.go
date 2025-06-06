// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package probes holds probes related files
package probes

import (
	manager "github.com/DataDog/ebpf-manager"
	"golang.org/x/sys/unix"
)

// GetTCProbes returns the list of TCProbes
func GetTCProbes(withNetworkIngress bool, withRawPacket bool) []*manager.Probe {
	out := []*manager.Probe{
		{
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				UID:          SecurityAgentUID,
				EBPFFuncName: "classifier_egress",
			},
			NetworkDirection: manager.Egress,
			TCFilterProtocol: unix.ETH_P_ALL,
			KeepProgramSpec:  true,
		},
	}

	if withRawPacket {
		out = append(out, &manager.Probe{
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				UID:          SecurityAgentUID,
				EBPFFuncName: "classifier_raw_packet_egress",
			},
			NetworkDirection: manager.Egress,
			TCFilterProtocol: unix.ETH_P_ALL,
			KeepProgramSpec:  true,
		})
	}

	if withNetworkIngress {
		out = append(out, &manager.Probe{
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				UID:          SecurityAgentUID,
				EBPFFuncName: "classifier_ingress",
			},
			NetworkDirection: manager.Ingress,
			TCFilterProtocol: unix.ETH_P_ALL,
			KeepProgramSpec:  true,
		})

		if withRawPacket {
			out = append(out, &manager.Probe{
				ProbeIdentificationPair: manager.ProbeIdentificationPair{
					UID:          SecurityAgentUID,
					EBPFFuncName: "classifier_raw_packet_ingress",
				},
				NetworkDirection: manager.Ingress,
				TCFilterProtocol: unix.ETH_P_ALL,
				KeepProgramSpec:  true,
			})
		}
	}

	return out
}

// GetRawPacketTCProgramFunctions returns the raw packet functions
func GetRawPacketTCProgramFunctions() []string {
	return []string{
		tailCallClassifierFnc("raw_packet"),
		tailCallClassifierFnc("raw_packet_sender"),
	}
}

// GetAllTCProgramFunctions returns the list of TC classifier sections
func GetAllTCProgramFunctions() []string {
	output := []string{
		tailCallClassifierFnc("dns_request_parser"),
		tailCallClassifierFnc("dns_response"),
		tailCallClassifierFnc("dns_request"),
		tailCallClassifierFnc("imds_request"),
	}

	output = append(output, GetRawPacketTCProgramFunctions()...)

	for _, tcProbe := range GetTCProbes(true, true) {
		output = append(output, tcProbe.EBPFFuncName)
	}

	for _, flowProbe := range getFlowProbes() {
		output = append(output, flowProbe.EBPFFuncName)
	}

	for _, netDeviceProbe := range getNetDeviceProbes() {
		output = append(output, netDeviceProbe.EBPFFuncName)
	}

	return output
}

func getTCTailCallRoutes(withRawPacket bool) []manager.TailCallRoute {
	tcr := []manager.TailCallRoute{
		{
			ProgArrayName: "classifier_router",
			Key:           TCDNSRequestKey,
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				EBPFFuncName: tailCallClassifierFnc("dns_request"),
			},
		},
		{
			ProgArrayName: "classifier_router",
			Key:           TCDNSRequestParserKey,
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				EBPFFuncName: tailCallClassifierFnc("dns_request_parser"),
			},
		},
		{
			ProgArrayName: "classifier_router",
			Key:           TCIMDSRequestParserKey,
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				EBPFFuncName: tailCallClassifierFnc("imds_request"),
			},
		},
		{
			ProgArrayName: "classifier_router",
			Key:           TCDNSResponseKey,
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				EBPFFuncName: tailCallClassifierFnc("dns_response"),
			},
		},
	}

	if withRawPacket {
		tcr = append(tcr, manager.TailCallRoute{
			ProgArrayName: "raw_packet_classifier_router",
			Key:           TCRawPacketParserSenderKey,
			ProbeIdentificationPair: manager.ProbeIdentificationPair{
				EBPFFuncName: tailCallClassifierFnc("raw_packet_sender"),
			},
		})
	}

	return tcr
}
