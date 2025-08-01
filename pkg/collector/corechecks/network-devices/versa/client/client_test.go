// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2025-present Datadog, Inc.

//go:build test

package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/network-devices/versa/client/fixtures"
	"github.com/stretchr/testify/require"
)

// TODO: add test for pagination if not moved to common functiton
func TestGetOrganizations(t *testing.T) {
	expectedOrgs := []Organization{
		{
			UUID:                    "fakeUUID",
			Name:                    "datadog",
			ParentOrg:               "fakeParentOrg",
			Connectors:              []string{"datadog-test", "datadog-other-test"},
			Plan:                    "Default-All-Services-Plan",
			GlobalOrgID:             "418", // Hyper Text Coffee Pot Control Protocol
			Description:             "DataDog Unit Test Fixture",
			SharedControlPlane:      true,
			BlockInterRegionRouting: true,
			CpeDeploymentType:       "SDWAN",
			AuthType:                "unitTest",
			ProviderOrg:             false,
			Depth:                   10,
			PushCaConfig:            false,
		},
		{
			UUID:                    "fakeUUID2",
			Name:                    "datadog2",
			ParentOrg:               "fakeParentOrg2",
			Connectors:              []string{"datadog-test", "datadog-other-test"},
			Plan:                    "Default-All-Services-Plan",
			GlobalOrgID:             "418", // Hyper Text Coffee Pot Control Protocol
			Description:             "DataDog Unit Test Fixture 2",
			SharedControlPlane:      false,
			BlockInterRegionRouting: false,
			CpeDeploymentType:       "SDWAN",
			AuthType:                "unitTest 2",
			ProviderOrg:             true,
			Depth:                   10,
			PushCaConfig:            true,
		},
	}

	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	require.NoError(t, err)

	actualOrgs, err := client.GetOrganizations()
	require.NoError(t, err)

	// Check contents
	require.Equal(t, 2, len(actualOrgs))
	require.Equal(t, expectedOrgs, actualOrgs)
}

func TestGetChildAppliancesDetail(t *testing.T) {
	expectedAppliances := []Appliance{
		{
			Name: "branch-1",
			UUID: "fakeUUID-branch-1",
			ApplianceLocation: ApplianceLocation{
				ApplianceName: "branch-1",
				ApplianceUUID: "fakeUUID-branch-1",
				LocationID:    "USA",
				Latitude:      "0.00",
				Longitude:     "0.00",
				Type:          "branch",
			},
			LastUpdatedTime:         "2025-04-24 20:26:11.0",
			PingStatus:              "UNREACHABLE",
			SyncStatus:              "UNKNOWN",
			YangCompatibilityStatus: "Unavailable",
			ServicesStatus:          "UNKNOWN",
			OverallStatus:           "NOT-APPLICABLE",
			PathStatus:              "Unavailable",
			IntraChassisHAStatus:    HAStatus{HAConfigured: false},
			InterChassisHAStatus:    HAStatus{HAConfigured: false},
			TemplateStatus:          "IN_SYNC",
			OwnerOrgUUID:            "another-fakeUUID-branch-1",
			OwnerOrg:                "datadog",
			Type:                    "branch",
			SngCount:                0,
			SoftwareVersion:         "Fake Version",
			BranchID:                "418",
			Services:                []string{"sdwan", "nextgen-firewall", "iot-security", "cgnat"},
			IPAddress:               "10.0.0.254",
			StartTime:               "Thu Jan  1 00:00:00 1970",
			StolenSuspected:         false,
			Hardware: Hardware{
				Name:                         "branch-1",
				Model:                        "Virtual Machine",
				CPUCores:                     0,
				Memory:                       "7.57GiB",
				FreeMemory:                   "3.81GiB",
				DiskSize:                     "90.34GiB",
				FreeDisk:                     "80.09GiB",
				LPM:                          false,
				Fanless:                      false,
				IntelQuickAssistAcceleration: false,
				FirmwareVersion:              "22.1.4",
				Manufacturer:                 "Microsoft Corporation",
				SerialNo:                     "fakeSerialNo-branch-1",
				HardWareSerialNo:             "fakeHardwareSerialNo-branch-1",
				CPUModel:                     "Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz",
				CPUCount:                     4,
				CPULoad:                      2,
				InterfaceCount:               1,
				PackageName:                  "versa-flexvnf-19700101",
				SKU:                          "Not Specified",
				SSD:                          false,
			},
			SPack: SPack{
				Name:         "branch-1",
				SPackVersion: "418",
				APIVersion:   "11",
				Flavor:       "sample",
				ReleaseDate:  "1970-01-01",
				UpdateType:   "full",
			},
			OssPack: OssPack{
				Name:           "branch-1",
				OssPackVersion: "OSSPACK Not Installed",
				UpdateType:     "None",
			},
			AppIDDetails: AppIDDetails{
				AppIDInstalledEngineVersion: "3.0.0-00 ",
				AppIDInstalledBundleVersion: "1.100.0-20 ",
			},
			RefreshCycleCount:       46232,
			SubType:                 "None",
			BranchMaintenanceMode:   false,
			ApplianceTags:           []string{"test"},
			ApplianceCapabilities:   CapabilitiesWrapper{Capabilities: []string{"path-state-monitor", "bw-in-interface-state", "config-encryption:v4", "route-filter-feature", "internet-speed-test:v1.2"}},
			Unreachable:             true,
			BranchInMaintenanceMode: false,
			Nodes: Nodes{
				NodeStatusList: NodeStatus{
					VMName:     "NOT-APPLICABLE",
					VMStatus:   "NOT-APPLICABLE",
					NodeType:   "VCSN",
					HostIP:     "NOT-APPLICABLE",
					CPULoad:    0,
					MemoryLoad: 0,
					LoadFactor: 0,
					SlotID:     0,
				},
			},
			UcpeNodes: UcpeNodes{UcpeNodeStatusList: []interface{}{}},
			AlarmSummary: Table{
				TableID:     "Alarms",
				TableName:   "Alarms",
				MonitorType: "Alarms",
				ColumnNames: []string{
					"columnName 0",
				},
				Rows: []TableRow{
					{
						FirstColumnValue: "critical",
						ColumnValues:     []interface{}{float64(2)},
					},
					{
						FirstColumnValue: "major",
						ColumnValues:     []interface{}{float64(2)},
					},
					{
						FirstColumnValue: "minor",
						ColumnValues:     []interface{}{float64(0)},
					},
					{
						FirstColumnValue: "warning",
						ColumnValues:     []interface{}{float64(0)},
					},
					{
						FirstColumnValue: "indeterminate",
						ColumnValues:     []interface{}{float64(0)},
					},
					{
						FirstColumnValue: "cleared",
						ColumnValues:     []interface{}{float64(6)},
					},
				},
			},
			CPEHealth: Table{
				TableName:   "Appliance Health",
				MonitorType: "Health",
				ColumnNames: []string{
					"Category",
					"Up",
					"Down",
				},
				Rows: []TableRow{
					{
						FirstColumnValue: "Physical Ports",
						ColumnValues:     []interface{}{float64(0), float64(0), float64(0)},
					},
					{
						FirstColumnValue: "Config Sync Status",
						ColumnValues:     []interface{}{float64(0), float64(1), float64(0)},
					},
					{
						FirstColumnValue: "Reachability Status",
						ColumnValues:     []interface{}{float64(0), float64(1), float64(0)},
					},
					{
						FirstColumnValue: "Service Status",
						ColumnValues:     []interface{}{float64(0), float64(1), float64(0)},
					},
					{
						FirstColumnValue: "Interfaces",
						ColumnValues:     []interface{}{float64(1), float64(0), float64(0)},
					},
					{
						FirstColumnValue: "BGP Adjacencies",
						ColumnValues:     []interface{}{float64(2), float64(0), float64(0)},
					},
					{
						FirstColumnValue: "IKE Status",
						ColumnValues:     []interface{}{float64(2), float64(0), float64(0)},
					},
					{
						FirstColumnValue: "Paths",
						ColumnValues:     []interface{}{float64(2), float64(0), float64(0)},
					},
				},
			},
			ApplicationStats: Table{
				TableID:     "App Activity",
				TableName:   "App Activity",
				MonitorType: "AppActivity",
				ColumnNames: []string{
					"App Name",
					"Sessions",
					"Transactions",
					"Total BytesForward",
					"TotalBytes Reverse",
				},
				Rows: []TableRow{
					{
						FirstColumnValue: "BITTORRENT",
						ColumnValues:     []interface{}{float64(1), float64(1), float64(0), float64(0)},
					},
					{
						FirstColumnValue: "ICMP",
						ColumnValues:     []interface{}{float64(1), float64(1), float64(0), float64(0)},
					},
				},
			},
			PolicyViolation: Table{
				TableID:     "Policy Violation",
				TableName:   "Policy Violation",
				MonitorType: "PolicyViolation",
				ColumnNames: []string{
					"Hit Count",
					"Packet drop no valid available link",
					"Packet drop attributed to SLA action",
					"Packet Forward attributed to SLA action",
				},
				Rows: []TableRow{
					{
						FirstColumnValue: "datadog",
						ColumnValues:     []interface{}{float64(0), float64(0), float64(0), float64(0)},
					},
				},
			},
		},
	}

	handler := func(w http.ResponseWriter, r *http.Request) {
		queryParams := r.URL.Query()
		if queryParams.Get("fetch") == "count" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`1`))
			return
		} else if queryParams.Get("fetch") == "all" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(fixtures.GetChildAppliancesDetail))
		}
	}

	mux := setupCommonServerMux()
	mux.HandleFunc("/vnms/dashboard/childAppliancesDetail/fakeTenant", handler)

	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := testClient(server)
	require.NoError(t, err)

	actualAppliances, err := client.GetChildAppliancesDetail("fakeTenant")
	require.NoError(t, err)

	// Check contents
	require.Equal(t, 1, len(actualAppliances))
	require.Equal(t, expectedAppliances, actualAppliances)
}

func TestGetDirectorStatus(t *testing.T) {
	expectedDirectorStatus := &DirectorStatus{
		HAConfig: DirectorHAConfig{
			ClusterID:                      "clusterId",
			FailoverTimeout:                100,
			SlaveStartTimeout:              300,
			AutoSwitchOverTimeout:          180,
			AutoSwitchOverEnabled:          false,
			DesignatedMaster:               true,
			StartupMode:                    "STANDALONE",
			MyVnfManagementIPs:             []string{"10.0.200.100"},
			VDSBInterfaces:                 []string{"10.0.201.100"},
			StartupModeHA:                  false,
			MyNcsHaSetAsMaster:             true,
			PingViaAnyDeviceSuccessful:     false,
			PeerReachableViaNcsPortDevices: true,
			HAEnabledOnBothNodes:           false,
		},
		HADetails: DirectorHADetails{
			Enabled:            false,
			DesignatedMaster:   true,
			PeerVnmsHaDetails:  []struct{}{},
			EnableHaInProgress: false,
		},
		VDSBInterfaces: []string{"10.0.201.100"},
		SystemDetails: DirectorSystemDetails{
			CPUCount:   32,
			CPULoad:    "2.11",
			Memory:     "64.01GB",
			MemoryFree: "20.10GB",
			Disk:       "128GB",
			DiskUsage:  "fakeDiskUsage",
		},
		PkgInfo: DirectorPkgInfo{
			Version:     "10.1",
			PackageDate: "1970101",
			Name:        "versa-director-1970101-000000-vissdf0cv-10.1.0-a",
			PackageID:   "vissdf0cv",
			UIPackageID: "versa-director-1970101-000000-vissdf0cv-10.1.0-a",
			Branch:      "10.1",
		},
		SystemUpTime: DirectorSystemUpTime{
			CurrentTime:       "Thu Jan 01 00:00:00 UTC 1970",
			ApplicationUpTime: "160 Days, 12 Hours, 56 Minutes, 35 Seconds.",
			SysProcUptime:     "230 Days, 17 Hours, 28 Minutes, 46 Seconds.",
			SysUpTimeDetail:   "20:45:35 up 230 days, 17:28,  1 users,  load average: 0.24, 0.16, 0.23",
		},
	}

	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	require.NoError(t, err)

	actualDirectorStatus, err := client.GetDirectorStatus()
	require.NoError(t, err)

	// Check contents
	require.Equal(t, expectedDirectorStatus, actualDirectorStatus)
}

func TestGetSLAMetrics(t *testing.T) {
	expectedSLAMetrics := []SLAMetrics{
		{
			DrillKey:            "test-branch-2B,Controller-2,INET-1,INET-1,fc_nc",
			LocalSite:           "test-branch-2B",
			RemoteSite:          "Controller-2",
			LocalAccessCircuit:  "INET-1",
			RemoteAccessCircuit: "INET-1",
			ForwardingClass:     "fc_nc",
			Delay:               101.0,
			FwdDelayVar:         0.0,
			RevDelayVar:         0.0,
			FwdLossRatio:        0.0,
			RevLossRatio:        0.0,
			PDULossRatio:        0.0,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	slaMetrics, err := client.GetSLAMetrics("datadog")
	require.NoError(t, err)

	require.Equal(t, len(slaMetrics), 1)
	require.Equal(t, expectedSLAMetrics, slaMetrics)
}

func TestGetLinkUsageMetrics(t *testing.T) {
	expectedLinkUsageMetrics := []LinkUsageMetrics{
		{
			DrillKey:          "test-branch-2B,INET-1",
			Site:              "test-branch-2B",
			AccessCircuit:     "INET-1",
			UplinkBandwidth:   "10000000000",
			DownlinkBandwidth: "10000000000",
			Type:              "Unknown",
			Media:             "Unknown",
			IP:                "10.20.20.7",
			ISP:               "",
			VolumeTx:          757144.0,
			VolumeRx:          457032.0,
			BandwidthTx:       6730.168888888889,
			BandwidthRx:       4062.5066666666667,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	linkUsageMetrics, err := client.GetLinkUsageMetrics("datadog")
	require.NoError(t, err)

	require.Equal(t, len(linkUsageMetrics), 1)
	require.Equal(t, expectedLinkUsageMetrics, linkUsageMetrics)
}

func TestGetLinkStatusMetrics(t *testing.T) {
	expectedLinkStatusMetrics := []LinkStatusMetrics{
		{
			DrillKey:      "test-branch-2B,INET-1",
			Site:          "test-branch-2B",
			AccessCircuit: "INET-1",
			Availability:  98.5,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	linkStatusMetrics, err := client.GetLinkStatusMetrics("datadog")
	require.NoError(t, err)

	require.Equal(t, len(linkStatusMetrics), 1)
	require.Equal(t, expectedLinkStatusMetrics, linkStatusMetrics)
}

func TestParseLinkUsageMetrics(t *testing.T) {
	testData := [][]interface{}{
		{
			"test-branch-2B,INET-1",
			"test-branch-2B",
			"INET-1",
			"10000000000",
			"10000000000",
			"Unknown",
			"Unknown",
			"10.20.20.7",
			"",
			757144.0,
			457032.0,
			6730.168888888889,
			4062.5066666666667,
		},
	}

	expected := []LinkUsageMetrics{
		{
			DrillKey:          "test-branch-2B,INET-1",
			Site:              "test-branch-2B",
			AccessCircuit:     "INET-1",
			UplinkBandwidth:   "10000000000",
			DownlinkBandwidth: "10000000000",
			Type:              "Unknown",
			Media:             "Unknown",
			IP:                "10.20.20.7",
			ISP:               "",
			VolumeTx:          757144.0,
			VolumeRx:          457032.0,
			BandwidthTx:       6730.168888888889,
			BandwidthRx:       4062.5066666666667,
		},
	}

	result, err := parseLinkUsageMetrics(testData)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestParseLinkStatusMetrics(t *testing.T) {
	testData := [][]interface{}{
		{
			"test-branch-2B,INET-1",
			"test-branch-2B",
			"INET-1",
			98.5,
		},
	}

	expected := []LinkStatusMetrics{
		{
			DrillKey:      "test-branch-2B,INET-1",
			Site:          "test-branch-2B",
			AccessCircuit: "INET-1",
			Availability:  98.5,
		},
	}

	result, err := parseLinkStatusMetrics(testData)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}
func TestGetApplicationsByAppliance(t *testing.T) {
	expectedApplicationsByApplianceMetrics := []ApplicationsByApplianceMetrics{
		{
			DrillKey:    "test-branch-2B,HTTP",
			Site:        "test-branch-2B",
			AppID:       "HTTP",
			Sessions:    50.0,
			VolumeTx:    1024000.0,
			VolumeRx:    512000.0,
			BandwidthTx: 8192.0,
			BandwidthRx: 4096.0,
			Bandwidth:   12288.0,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	appsByApplianceMetrics, err := client.GetApplicationsByAppliance("datadog")
	require.NoError(t, err)

	require.Equal(t, len(appsByApplianceMetrics), 1)
	require.Equal(t, expectedApplicationsByApplianceMetrics, appsByApplianceMetrics)
}

func TestGetTunnelMetrics(t *testing.T) {
	expectedTunnelMetrics := []TunnelMetrics{
		{
			DrillKey:    "test-branch-2B,10.1.1.1",
			Appliance:   "test-branch-2B",
			LocalIP:     "10.1.1.1",
			RemoteIP:    "10.2.2.2",
			VpnProfName: "vpn-profile-1",
			VolumeRx:    67890.0,
			VolumeTx:    12345.0,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	tunnelMetrics, err := client.GetTunnelMetrics("datadog")
	require.NoError(t, err)

	require.Equal(t, len(tunnelMetrics), 1)
	require.Equal(t, expectedTunnelMetrics, tunnelMetrics)
}

func TestParseTunnelMetrics(t *testing.T) {
	testData := [][]interface{}{
		{
			"test-branch-2B,10.1.1.1",
			"test-branch-2B",
			"10.1.1.1",
			"10.2.2.2",
			"vpn-profile-1",
			67890.0,
			12345.0,
		},
	}

	expected := []TunnelMetrics{
		{
			DrillKey:    "test-branch-2B,10.1.1.1",
			Appliance:   "test-branch-2B",
			LocalIP:     "10.1.1.1",
			RemoteIP:    "10.2.2.2",
			VpnProfName: "vpn-profile-1",
			VolumeRx:    67890.0,
			VolumeTx:    12345.0,
		},
	}

	result, err := parseTunnelMetrics(testData)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestGetTunnelMetricsEmptyTenant(t *testing.T) {
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	require.NoError(t, err)

	_, err = client.GetTunnelMetrics("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "tenant cannot be empty")
}

func TestGetTopUsers(t *testing.T) {
	expectedTopUsers := []TopUserMetrics{
		{
			DrillKey:    "test-branch-2B,testUser",
			Site:        "test-branch-2B",
			User:        "testUser",
			Sessions:    50.0,
			VolumeTx:    2024000.0,
			VolumeRx:    412000.0,
			BandwidthTx: 7192.0,
			BandwidthRx: 2096.0,
			Bandwidth:   22288.0,
		},
	}
	server := SetupMockAPIServer()
	defer server.Close()

	client, err := testClient(server)
	// TODO: remove this override when single auth
	// method is being used
	client.directorEndpoint = server.URL
	require.NoError(t, err)

	topUsers, err := client.GetTopUsers("datadog")
	require.NoError(t, err)

	require.Equal(t, len(topUsers), 1)
	require.Equal(t, expectedTopUsers, topUsers)
}
