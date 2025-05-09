// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022-present Datadog, Inc.

package apikey

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"os"
	"strings"

	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/fips"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// decryptFunc is a function that takes in a value and retrieves it from
// the appropriate AWS service. KMS, SM, etc.
type decryptFunc func(string, aws.FIPSEndpointState) (string, error)

func getSecretEnvVars(envVars []string, kmsFunc decryptFunc, smFunc decryptFunc, shouldUseFips bool) map[string]string {
	fipsEndpointState := aws.FIPSEndpointStateUnset
	if shouldUseFips {
		fipsEndpointState = aws.FIPSEndpointStateEnabled
		log.Debug("Using FIPS endpoints for secrets management.")
	}

	decryptedEnvVars := make(map[string]string)
	for _, envVar := range envVars {
		envKey, envVal, ok := strings.Cut(envVar, "=")
		if !ok {
			continue
		}
		if len(envVal) == 0 {
			continue
		}
		if strings.HasSuffix(envKey, kmsKeySuffix) {
			log.Debugf("Decrypting %v", envVar)
			secretVal, err := kmsFunc(envVal, fipsEndpointState)
			if err != nil {
				log.Errorf("Couldn't read API key from KMS: %v", err)
				continue
			}
			decryptedEnvVars[strings.TrimSuffix(envKey, kmsKeySuffix)] = secretVal
		}
		if envKey == apiKeyKmsEnvVar {
			secretVal, err := kmsFunc(envVal, fipsEndpointState)
			if err != nil {
				log.Errorf("Couldn't read API key from KMS: %v", err)
				continue
			}
			decryptedEnvVars[apiKeyEnvVar] = secretVal
		}
		if strings.HasSuffix(envKey, secretArnSuffix) {
			log.Debugf("Retrieving %v from secrets manager", envVar)
			secretVal, err := smFunc(envVal, fipsEndpointState)
			if err != nil {
				log.Errorf("Couldn't read API key from Secrets Manager: %v", err)
				continue
			}
			decryptedEnvVars[strings.TrimSuffix(envKey, secretArnSuffix)] = secretVal
		}
	}
	return decryptedEnvVars
}

// setSecretsFromEnv - The agent is going to get any environment variables ending with _KMS_ENCRYPTED and _SECRET_ARN,
// get the contents of the environment variable, and query SM/KMS to retrieve the value. This allows us
// to read arbitrarily encrypted environment variables and use the decrypted version in the extension.
// Right now, this feature is used for dual shipping, since customers need to set DD_LOGS_CONFIGURATION
// and a few other variables, which include an API key. The user can set DD_LOGS_CONFIGURATION_SECRET_ARN
// or DD_LOGS_CONFIGURATION_KMS_ENCRYPTED, which will get converted in the extension to a plaintext
// DD_LOGS_CONFIGURATION, and will have dual shipping enabled without exposing
// their API key in plaintext through environment variables.
func setSecretsFromEnv(envVars []string) {
	shouldUseFips, err := fips.Enabled()
	if err != nil {
		log.Debugf("Could not determine if FIPS is enabled, assuming it is not: %v", err)
		shouldUseFips = false
	}

	for envKey, envVal := range getSecretEnvVars(envVars, readAPIKeyFromKMS, readAPIKeyFromSecretsManager, shouldUseFips) {
		os.Setenv(envKey, strings.TrimSpace(envVal))
	}
}

// HandleEnv sets the API key from environment variables
func HandleEnv() error {
	// API key reading
	// ---------------

	// API key reading priority:
	// Plaintext > KMS > Secrets Manager
	// If an API key is set but failing, the next will be tried

	checkForSingleAPIKey()

	// Set API key from DD_KMS_API_KEY environment variable or environment variables suffixed with KMS_ENCRYPTED or SECRET_ARN
	setSecretsFromEnv(os.Environ())

	// Validate that an API key has been set, either by DD_API_KEY or read from KMS or Secrets Manager
	// ---------------------------
	if !pkgconfigsetup.Datadog().IsSet("api_key") {
		// we're not reporting the error to AWS because we don't want the function
		// execution to be stopped. TODO(remy): discuss with AWS if there is way
		// of reporting non-critical init errors.
		return log.Error("No API key configured")
	}
	return nil
}
