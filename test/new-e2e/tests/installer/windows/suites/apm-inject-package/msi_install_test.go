// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package injecttests

import (
	"fmt"

	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/e2e"
	winawshost "github.com/DataDog/datadog-agent/test/new-e2e/pkg/provisioners/aws/host/windows"
	installer "github.com/DataDog/datadog-agent/test/new-e2e/tests/installer/unix"
	installerwindows "github.com/DataDog/datadog-agent/test/new-e2e/tests/installer/windows"
	"github.com/DataDog/datadog-agent/test/new-e2e/tests/installer/windows/consts"

	"testing"
)

type testAgentMSIInstallsAPMInject struct {
	baseSuite
}

// TestAgentScriptInstallsAPMInject tests the usage of the install script to install the apm-inject package.
func TestAgentMSIInstallsAPMInject(t *testing.T) {
	e2e.Run(t, &testAgentMSIInstallsAPMInject{},
		e2e.WithProvisioner(
			winawshost.ProvisionerNoAgentNoFakeIntake()))
}

func (s *testAgentMSIInstallsAPMInject) AfterTest(suiteName, testName string) {
	s.Installer().Purge()
	s.baseSuite.AfterTest(suiteName, testName)
}

// TestInstallFromMSI tests the Agent MSI can install the APM inject package with host instrumentation
func (s *testAgentMSIInstallsAPMInject) TestInstallFromMSI() {
	// Act
	s.installCurrentAgentVersion(
		installerwindows.WithMSIArg("DD_APM_INSTRUMENTATION_ENABLED=host"),
		// TODO: remove override once image is published in prod
		installerwindows.WithMSIArg("DD_INSTALLER_REGISTRY_URL=install.datad0g.com"),
		installerwindows.WithMSIArg(fmt.Sprintf("DD_INSTALLER_DEFAULT_PKG_VERSION_DATADOG_APM_INJECT=%s", s.currentAPMInjectVersion.PackageVersion())),
		installerwindows.WithMSIArg("DD_APM_INSTRUMENTATION_LIBRARIES=java:1"),
		installerwindows.WithMSILogFile("install.log"),
	)

	// Verify the package is installed
	s.assertSuccessfulPromoteExperiment()

	s.assertDriverInjections()
}

// installCurrentAgentVersionWithAPMInject installs the current agent version with APM inject via script
func (s *testAgentMSIInstallsAPMInject) installCurrentAgentVersion(opts ...installerwindows.MsiOption) {
	agentVersion := s.CurrentAgentVersion().Version()

	options := []installerwindows.MsiOption{
		installerwindows.WithOption(installerwindows.WithInstallerURL(s.CurrentAgentVersion().MSIPackage().URL)),
		installerwindows.WithMSILogFile("install-current-version.log"),
		installerwindows.WithMSIArg(fmt.Sprintf("APIKEY=%s", installer.GetAPIKey())),
		installerwindows.WithMSIArg("SITE=datadoghq.com"),
	}
	options = append(options, opts...)
	s.Require().NoError(s.Installer().Install(
		options...,
	))

	// sanity check: make sure we did indeed install the stable version
	s.Require().Host(s.Env().RemoteHost).
		HasBinary(consts.BinaryPath).
		// Don't check the binary signature because it could have been updated since the last stable was built
		WithVersionMatchPredicate(func(version string) {
			s.Require().Contains(version, agentVersion)
		})
}
