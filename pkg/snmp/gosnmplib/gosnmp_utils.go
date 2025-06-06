// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package gosnmplib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/DataDog/datadog-agent/pkg/util/log"
)

type debugVariable struct {
	Oid      string      `json:"oid"`
	Type     string      `json:"type"`
	Value    interface{} `json:"value"`
	ParseErr string      `json:"parse_err,omitempty"`
}

var strippableSpecialChars = map[byte]bool{'\r': true, '\n': true, '\t': true, 00: true}

// IsStringPrintable returns true if the provided byte array is only composed of printable characeters
func IsStringPrintable(bytesValue []byte) bool {
	for _, bit := range bytesValue {
		if bit < 32 || bit > 126 {
			// The char is not a printable ASCII char but it might be a character that
			// can be stripped like `\n`
			if _, ok := strippableSpecialChars[bit]; !ok {
				return false
			}
		}
	}
	return true
}

// GetValueFromPDU converts the value from an SnmpPDU to a standard type.
// Octet and Bit strings will be returned as []byte.
// All integer and float types will be returned as float64.
// IPs and OIDs will be returned as strings; OIDs will have any leading '.' stripped.
// Unsupported types:
//   - gosnmp.Opaque: gosnmp never returns these, instead processing them recursively
//     See https://github.com/gosnmp/gosnmp/blob/dc320dac5b53d95a366733fd95fb5851f2099387/helper.go#L195-L205
//   - Boolean, Null: Although ASN1 allows these, SNMP does not (if someone needs a bool they use an enumerated integer instead)
func GetValueFromPDU(pduVariable gosnmp.SnmpPDU) (interface{}, error) {
	switch pduVariable.Type {
	case gosnmp.OctetString, gosnmp.BitString:
		bytesValue, ok := pduVariable.Value.([]byte)
		if !ok {
			return nil, fmt.Errorf("oid %s: OctetString/BitString should be []byte type but got type `%T` and value `%v`", pduVariable.Name, pduVariable.Value, pduVariable.Value)
		}
		return bytesValue, nil
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return float64(gosnmp.ToBigInt(pduVariable.Value).Int64()), nil
	case gosnmp.OpaqueFloat:
		floatValue, ok := pduVariable.Value.(float32)
		if !ok {
			return nil, fmt.Errorf("oid %s: OpaqueFloat should be float32 type but got type `%T` and value `%v`", pduVariable.Name, pduVariable.Value, pduVariable.Value)
		}
		return float64(floatValue), nil
	case gosnmp.OpaqueDouble:
		floatValue, ok := pduVariable.Value.(float64)
		if !ok {
			return nil, fmt.Errorf("oid %s: OpaqueDouble should be float64 type but got type `%T` and value `%v`", pduVariable.Name, pduVariable.Value, pduVariable.Value)
		}
		return floatValue, nil
	case gosnmp.IPAddress:
		strValue, ok := pduVariable.Value.(string)
		if !ok {
			return nil, fmt.Errorf("oid %s: IPAddress should be string type but got type `%T` and value `%v`", pduVariable.Name, pduVariable.Value, pduVariable.Value)
		}
		return strValue, nil
	case gosnmp.ObjectIdentifier:
		strValue, ok := pduVariable.Value.(string)
		if !ok {
			return nil, fmt.Errorf("oid %s: ObjectIdentifier should be string type but got type `%T` and value `%v`", pduVariable.Name, pduVariable.Value, pduVariable.Value)
		}
		return strings.TrimLeft(strValue, "."), nil
	default:
		return nil, fmt.Errorf("oid %s: invalid type: %s", pduVariable.Name, pduVariable.Type.String())
	}
}

// StandardTypeToString can be used to convert the output of `GetValueFromPDU` to a string
func StandardTypeToString(value interface{}) (string, error) {
	switch value := value.(type) {
	case float64:
		return strconv.Itoa(int(value)), nil
	case string:
		return value, nil
	case []byte:
		bytesValue := value
		if !IsStringPrintable(bytesValue) {
			// We hexify like Python/pysnmp impl (keep compatibility) if the value contains non ascii letters:
			// https://github.com/etingof/pyasn1/blob/db8f1a7930c6b5826357646746337dafc983f953/pyasn1/type/univ.py#L950-L953
			// hexifying like pysnmp prettyPrint might lead to unpredictable results since `[]byte` might or might not have
			// elements outside of 32-126 range. New lines, tabs and carriage returns are also stripped from the string.
			// An alternative solution is to explicitly force the conversion to specific type using profile config.
			return hexify(bytesValue), nil
		}

		// String is printable, trimming trailing 00s
		trimmedBytes := bytes.TrimRight(bytesValue, "\x00")
		if len(trimmedBytes) == 0 {
			// If value is only 0s, hexify
			return hexify(bytesValue), nil
		}

		return string(trimmedBytes), nil
	}

	return "", fmt.Errorf("invalid type %T for value %#v", value, value)
}

func hexify(bytesValue []byte) string {
	return fmt.Sprintf("%#x", bytesValue)
}

// PacketAsString used to format gosnmp.SnmpPacket for debug/trace logging
func PacketAsString(packet *gosnmp.SnmpPacket) string {
	if packet == nil {
		return ""
	}
	var debugVariables []debugVariable
	for _, pduVariable := range packet.Variables {
		var parseError string
		name := pduVariable.Name
		value := fmt.Sprintf("%v", pduVariable.Value)
		resValue, err := GetValueFromPDU(pduVariable)
		if err == nil {
			resValueStr, err := StandardTypeToString(resValue)
			if err == nil {
				value = resValueStr
			}
		}
		if err != nil {
			parseError = fmt.Sprintf("`%s`", err)
		}
		debugVar := debugVariable{Oid: name, Type: fmt.Sprintf("%v", pduVariable.Type), Value: value, ParseErr: parseError}
		debugVariables = append(debugVariables, debugVar)
	}

	jsonPayload, err := json.Marshal(debugVariables)
	if err != nil {
		log.Debugf("error marshaling debugVar: %s", err)
		jsonPayload = []byte(``)
	}
	return fmt.Sprintf("error=%s(code:%d, idx:%d), values=%s", packet.Error, packet.Error, packet.ErrorIndex, jsonPayload)
}
