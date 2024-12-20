// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package parameters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/utils/common"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmTypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/DataDog/datadog-agent/test/new-e2e/pkg/utils/clients"
)

var _ valueStore = &awsStore{}

type awsStore struct {
	prefix string
}

// NewAWSStore creates a new AWS store
func NewAWSStore(prefix string) Store {
	return newStore(newCachingStore(awsStore{
		prefix: prefix,
	}))
}

// Get returns parameter value.
// For AWS Store, parameter key is lowered and added to prefix
func (s awsStore) get(key StoreKey) (string, error) {
	ssmClient, err := clients.GetAWSSSMClient()
	if err != nil {
		return "", err
	}
	if newKey, ok := awsOverrides[key]; ok {
		key = newKey
	}

	awsKey := strings.ToLower(s.prefix + string(key))
	withDecryption := true
	output, err := ssmClient.GetParameter(context.Background(), &ssm.GetParameterInput{Name: &awsKey, WithDecryption: &withDecryption})
	if err != nil {
		var notFoundError *ssmTypes.ParameterNotFound
		if errors.As(err, &notFoundError) {
			return "", ParameterNotFoundError{key: key}
		}

		return "", common.InternalError{Err: fmt.Errorf("failed to get SSM parameter '%s', err: %w", awsKey, err)}
	}

	return *output.Parameter.Value, nil
}

// awsOverrides is a map of StoreKey to StoreKey used to override key only in AWS store
var awsOverrides = map[StoreKey]StoreKey{
	APIKey: "api_key_3",
	APPKey: "app_key_2",
}
