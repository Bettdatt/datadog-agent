// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022-present Datadog, Inc.

//go:build test

package oidresolverimpl

import (
	"fmt"

	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/snmptraps/oidresolver"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
)

// MockModule provides a dummy resolver with canned data.
// Set your own data with fx.Replace(&oidresolver.TrapDBFileContent{...})
func MockModule() fxutil.Module {
	return fxutil.Component(
		fx.Provide(NewMockResolver),
		fx.Supply(&dummyTrapDB),
	)
}

// MockResolver implements OIDResolver with a mock database.
type MockResolver struct {
	content *oidresolver.TrapDBFileContent
}

// GetTrapMetadata implements OIDResolver#GetTrapMetadata.
func (r MockResolver) GetTrapMetadata(trapOid string) (oidresolver.TrapMetadata, error) {
	trapOid = oidresolver.NormalizeOID(trapOid)
	trapData, ok := r.content.Traps[trapOid]
	if !ok {
		return oidresolver.TrapMetadata{}, fmt.Errorf("trap OID %s is not defined", trapOid)
	}
	return trapData, nil
}

// GetVariableMetadata implements OIDResolver#GetVariableMetadata.
func (r MockResolver) GetVariableMetadata(_ string, varOid string) (oidresolver.VariableMetadata, error) { //nolint:revive // TODO fix revive unusued-parameter
	varOid = oidresolver.NormalizeOID(varOid)
	varData, ok := r.content.Variables[varOid]
	if !ok {
		return oidresolver.VariableMetadata{}, fmt.Errorf("variable OID %s is not defined", varOid)
	}
	return varData, nil
}

// NewMockResolver creates a mock resolver populated with fake data.
func NewMockResolver(content *oidresolver.TrapDBFileContent) oidresolver.Component {
	return &MockResolver{content}
}

var dummyTrapDB = oidresolver.TrapDBFileContent{
	Traps: oidresolver.TrapSpec{
		"1.3.6.1.6.3.1.1.5.3":      oidresolver.TrapMetadata{Name: "ifDown", MIBName: "IF-MIB"},                                             // v1 Trap
		"1.3.6.1.4.1.8072.2.3.0.1": oidresolver.TrapMetadata{Name: "netSnmpExampleHeartbeatNotification", MIBName: "NET-SNMP-EXAMPLES-MIB"}, // v2+
		"1.3.6.1.6.3.1.1.5.4":      oidresolver.TrapMetadata{Name: "linkUp", MIBName: "IF-MIB"},
	},
	Variables: oidresolver.VariableSpec{
		"1.3.6.1.2.1.2.2.1.1":      oidresolver.VariableMetadata{Name: "ifIndex"},
		"1.3.6.1.2.1.2.2.1.7":      oidresolver.VariableMetadata{Name: "ifAdminStatus", Enumeration: map[int]string{1: "up", 2: "down", 3: "testing"}},
		"1.3.6.1.2.1.2.2.1.8":      oidresolver.VariableMetadata{Name: "ifOperStatus", Enumeration: map[int]string{1: "up", 2: "down", 3: "testing", 4: "unknown", 5: "dormant", 6: "notPresent", 7: "lowerLayerDown"}},
		"1.3.6.1.4.1.8072.2.3.2.1": oidresolver.VariableMetadata{Name: "netSnmpExampleHeartbeatRate"},
		"1.3.6.1.2.1.200.1.1.1.3": oidresolver.VariableMetadata{Name: "pwCepSonetConfigErrorOrStatus", Bits: map[int]string{
			0:  "other",
			1:  "timeslotInUse",
			2:  "timeslotMisuse",
			3:  "peerDbaIncompatible",
			4:  "peerEbmIncompatible",
			5:  "peerRtpIncompatible",
			6:  "peerAsyncIncompatible",
			7:  "peerDbaAsymmetric",
			8:  "peerEbmAsymmetric",
			9:  "peerRtpAsymmetric",
			10: "peerAsyncAsymmetric",
		}},
		"1.3.6.1.2.1.200.1.3.1.5": oidresolver.VariableMetadata{Name: "myFakeVarType", Bits: map[int]string{
			0:   "test0",
			1:   "test1",
			3:   "test3",
			5:   "test5",
			6:   "test6",
			12:  "test12",
			15:  "test15",
			130: "test130",
		}},
		"1.3.6.1.2.1.200.1.3.1.6": oidresolver.VariableMetadata{
			Name: "myBadVarType",
			Enumeration: map[int]string{
				0:   "test0",
				1:   "test1",
				3:   "test3",
				5:   "test5",
				6:   "test6",
				12:  "test12",
				15:  "test15",
				130: "test130",
			},
			Bits: map[int]string{
				0:   "test0",
				1:   "test1",
				3:   "test3",
				5:   "test5",
				6:   "test6",
				12:  "test12",
				15:  "test15",
				130: "test130",
			},
		},
	},
}
