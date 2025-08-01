// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package defaultforwarder

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	"github.com/DataDog/datadog-agent/comp/forwarder/defaultforwarder/endpoints"
	"github.com/DataDog/datadog-agent/comp/forwarder/defaultforwarder/resolver"
	"github.com/DataDog/datadog-agent/comp/forwarder/defaultforwarder/transaction"
	mock "github.com/DataDog/datadog-agent/pkg/config/mock"
	pkgconfigmodel "github.com/DataDog/datadog-agent/pkg/config/model"
	configUtils "github.com/DataDog/datadog-agent/pkg/config/utils"
	"github.com/DataDog/datadog-agent/pkg/version"
)

var (
	testDomain           = "http://app.datadoghq.com"
	testVersionDomain, _ = configUtils.AddAgentVersionToDomain(testDomain, "app")
	monoKeysDomains      = map[string][]configUtils.APIKeys{
		testVersionDomain: {configUtils.NewAPIKeys("path", "monokey")},
	}
	keysPerDomains = map[string][]configUtils.APIKeys{
		testDomain: {
			configUtils.NewAPIKeys("path", "api-key-1", "api-key-2"),
		},
		"datadog.bar": nil,
	}
	keysWithMultipleDomains = map[string][]configUtils.APIKeys{
		testDomain: {
			configUtils.NewAPIKeys("path", "api-key-1"),
			configUtils.NewAPIKeys("path", "api-key-2"),
		},
		"datadog.bar": {configUtils.NewAPIKeys("path", "api-key-3")},
	}
	validKeysPerDomain = map[string][]configUtils.APIKeys{
		testVersionDomain: {configUtils.NewAPIKeys("path", "api-key-1", "api-key-2")},
	}
)

func TestNewDefaultForwarder(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))

	assert.NotNil(t, forwarder)
	assert.Equal(t, 1, forwarder.NumberOfWorkers)
	require.Len(t, forwarder.domainForwarders, 1) // only one domain has keys
	r, err = resolver.NewSingleDomainResolvers(validKeysPerDomain)
	require.NoError(t, err)
	assert.Equal(t, r, forwarder.domainResolvers)
	assert.Len(t, forwarder.domainForwarders, 1) // datadog.bar should have been dropped

	assert.Equal(t, forwarder.internalState.Load(), Stopped)
	assert.Equal(t, forwarder.State(), forwarder.internalState.Load())

	mockConfig.SetWithoutSource("autoscaling.failover.enabled", true)
	mockConfig.SetWithoutSource("cluster_agent.enabled", true)
	localDomain := "https://localhost"
	localAuth := "tokenABCD12345678910109876543210"
	mockConfig.SetWithoutSource("cluster_agent.url", localDomain)
	mockConfig.SetWithoutSource("cluster_agent.auth_token", localAuth)
	r, err = resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder2 := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	assert.NotNil(t, forwarder2)
	assert.Equal(t, 1, forwarder2.NumberOfWorkers)
	require.Len(t, forwarder2.domainForwarders, 2) // 1 remote domain, 1 dca domain
	domainResolver, _ := resolver.NewSingleDomainResolvers(validKeysPerDomain)
	domainResolver[localDomain] = resolver.NewLocalDomainResolver(localDomain, localAuth)
	assert.Equal(t, domainResolver, forwarder2.domainResolvers)
	assert.Equal(t, forwarder2.internalState.Load(), Stopped)
	assert.Equal(t, forwarder2.State(), forwarder2.internalState.Load())
}

func TestFeature(t *testing.T) {
	var featureSet Features

	featureSet = SetFeature(featureSet, CoreFeatures)
	featureSet = SetFeature(featureSet, ProcessFeatures)
	assert.True(t, HasFeature(featureSet, CoreFeatures))
	assert.True(t, HasFeature(featureSet, ProcessFeatures))

	featureSet = ClearFeature(featureSet, CoreFeatures)
	assert.False(t, HasFeature(featureSet, CoreFeatures))
	assert.True(t, HasFeature(featureSet, ProcessFeatures))

	featureSet = ToggleFeature(featureSet, ProcessFeatures)
	assert.False(t, HasFeature(featureSet, CoreFeatures))
	assert.False(t, HasFeature(featureSet, ProcessFeatures))
}

func TestStart(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(monoKeysDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	err = forwarder.Start()
	defer forwarder.Stop()

	assert.NoError(t, err)
	assert.Equal(t, Started, forwarder.State())
	require.Len(t, forwarder.domainForwarders, 1)
	require.NotNil(t, forwarder.healthChecker)
	assert.NotNil(t, forwarder.Start())
}

func TestStopWithoutPurgingTransaction(t *testing.T) {
	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("forwarder_stop_timeout", 0)

	testStop(t, mockConfig)
}

func TestStopWithPurgingTransaction(t *testing.T) {
	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("forwarder_stop_timeout", 1)

	testStop(t, mockConfig)
}

func testStop(t *testing.T, mockConfig pkgconfigmodel.Config) {
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	assert.Equal(t, Stopped, forwarder.State())
	forwarder.Stop() // this should be a noop
	forwarder.Start()
	domainForwarders := forwarder.domainForwarders
	forwarder.Stop()
	assert.Equal(t, Stopped, forwarder.State())
	assert.Nil(t, forwarder.healthChecker)
	assert.Len(t, forwarder.domainForwarders, 0)
	for _, df := range domainForwarders {
		assert.Equal(t, Stopped, df.internalState)
	}
}

func TestSubmitIfStopped(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(monoKeysDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))

	require.NotNil(t, forwarder)
	require.Equal(t, Stopped, forwarder.State())
	assert.NotNil(t, forwarder.SubmitSketchSeries(nil, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitHostMetadata(nil, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitMetadata(nil, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitV1Series(nil, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitSeries(nil, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitV1Intake(nil, transaction.Series, make(http.Header)))
	assert.NotNil(t, forwarder.SubmitV1CheckRuns(nil, make(http.Header)))
}

func TestCreateHTTPTransactions(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "foo"}
	p1 := []byte("A payload")
	p2 := []byte("Another payload")
	payloads := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p1, &p2})
	headers := make(http.Header)
	headers.Set("HTTP-MAGIC", "foo")

	transactions := forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)
	require.Len(t, transactions, 4)
	assert.Equal(t, testVersionDomain, transactions[0].Domain)
	assert.Equal(t, testVersionDomain, transactions[1].Domain)
	assert.Equal(t, testVersionDomain, transactions[2].Domain)
	assert.Equal(t, testVersionDomain, transactions[3].Domain)
	assert.Equal(t, endpoint.Route, transactions[0].Endpoint.Route)
	assert.Equal(t, endpoint.Route, transactions[1].Endpoint.Route)
	assert.Equal(t, endpoint.Route, transactions[2].Endpoint.Route)
	assert.Equal(t, endpoint.Route, transactions[3].Endpoint.Route)
	assert.Len(t, transactions[0].Headers, 4)
	assert.NotEmpty(t, transactions[0].Headers.Get("DD-Api-Key"))
	assert.NotEmpty(t, transactions[0].Headers.Get("HTTP-MAGIC"))
	assert.Equal(t, version.AgentVersion, transactions[0].Headers.Get("DD-Agent-Version"))
	assert.Equal(t, "datadog-agent/"+version.AgentVersion, transactions[0].Headers.Get("User-Agent"))
	assert.Equal(t, "", transactions[0].Headers.Get(arbitraryTagHTTPHeaderKey))
	assert.Equal(t, p1, transactions[0].Payload.GetContent())
	assert.Equal(t, p1, transactions[1].Payload.GetContent())
	assert.Equal(t, p2, transactions[2].Payload.GetContent())
	assert.Equal(t, p2, transactions[3].Payload.GetContent())
}

func TestCreateHTTPTransactionsWithMultipleDomains(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysWithMultipleDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "foo"}
	p1 := []byte("A payload")
	payloads := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p1})
	headers := make(http.Header)
	headers.Set("HTTP-MAGIC", "foo")

	transactions := forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)
	require.Len(t, transactions, 3, "should contain 3 transactions, contains %d", len(transactions))

	var txNormal, txBar []*transaction.HTTPTransaction
	for _, t := range transactions {
		if t.Domain == testVersionDomain {
			txNormal = append(txNormal, t)
		}
		if t.Domain == "datadog.bar" {
			txBar = append(txBar, t)
		}
	}

	assert.Equal(t, len(txNormal), 2, "Two transactions should target the normal domain")
	assert.Equal(t, len(txBar), 1, "One transactions should target the normal domain")

	if txNormal[0].Headers.Get("DD-Api-Key") == "api-key-1" {
		assert.Equal(t, txNormal[0].Headers.Get("DD-Api-Key"), "api-key-1")
		assert.Equal(t, txNormal[1].Headers.Get("DD-Api-Key"), "api-key-2")
	} else {
		assert.Equal(t, txNormal[0].Headers.Get("DD-Api-Key"), "api-key-2")
		assert.Equal(t, txNormal[1].Headers.Get("DD-Api-Key"), "api-key-1")
	}

	assert.Equal(t, txBar[0].Headers.Get("DD-Api-Key"), "api-key-3")
}

func TestCreateHTTPTransactionsWithDifferentResolvers(t *testing.T) {
	resolvers, err := resolver.NewSingleDomainResolvers(keysWithMultipleDomains)
	require.NoError(t, err)
	additionalResolver, err := resolver.NewMultiDomainResolver("datadog.vector", []configUtils.APIKeys{configUtils.NewAPIKeys("path", "api-key-4")})
	require.NoError(t, err)
	additionalResolver.RegisterAlternateDestination("diversion.domain", "diverted_name", resolver.Vector)
	resolvers["datadog.vector"] = additionalResolver
	mockConfig := mock.New(t)
	log := logmock.New(t)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, resolvers))
	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "diverted_name"}
	p1 := []byte("A payload")
	payloads := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p1})
	headers := make(http.Header)
	headers.Set("HTTP-MAGIC", "foo")

	transactions := forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)
	require.Len(t, transactions, 4, "should contain 4 transactions, contains %d", len(transactions))

	var txNormal, txBar, txVector []*transaction.HTTPTransaction
	for _, t := range transactions {
		if t.Domain == testVersionDomain {
			txNormal = append(txNormal, t)
		}
		if t.Domain == "datadog.bar" {
			txBar = append(txBar, t)
		}
		if t.Domain == "diversion.domain" {
			txVector = append(txVector, t)
		}
	}

	assert.Equal(t, len(txNormal), 2, "Two transactions should target the normal domain")
	assert.Equal(t, len(txBar), 1, "One transactions should target the normal domain")

	if txNormal[0].Headers.Get("DD-Api-Key") == "api-key-1" {
		assert.Equal(t, txNormal[0].Headers.Get("DD-Api-Key"), "api-key-1")
		assert.Equal(t, txNormal[1].Headers.Get("DD-Api-Key"), "api-key-2")
	} else {
		assert.Equal(t, txNormal[0].Headers.Get("DD-Api-Key"), "api-key-2")
		assert.Equal(t, txNormal[1].Headers.Get("DD-Api-Key"), "api-key-1")
	}
	assert.Equal(t, txBar[0].Headers.Get("DD-Api-Key"), "api-key-3")
	assert.Equal(t, txVector[0].Headers.Get("DD-Api-Key"), "api-key-4")
}

func TestCreateHTTPTransactionsWithOverrides(t *testing.T) {
	resolvers := make(map[string]resolver.DomainResolver)
	r, err := resolver.NewMultiDomainResolver(testDomain, []configUtils.APIKeys{configUtils.NewAPIKeys("path", "api-key-1")})
	require.NoError(t, err)
	r.RegisterAlternateDestination("observability_pipelines_worker.tld", "diverted", resolver.Vector)
	resolvers[testDomain] = r
	mockConfig := mock.New(t)
	log := logmock.New(t)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, resolvers))

	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "no_diverted"}
	p1 := []byte("A payload")
	payloads := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p1})
	headers := make(http.Header)
	headers.Set("HTTP-MAGIC", "foo")

	transactions := forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)
	require.Len(t, transactions, 1, "should contain 1 transaction, contains %d", len(transactions))

	assert.Equal(t, transactions[0].Endpoint.Route, "/api/foo")
	assert.Equal(t, transactions[0].Domain, testVersionDomain)

	endpoint.Name = "diverted"
	transactions = forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)
	require.Len(t, transactions, 1, "should contain 1 transaction, contains %d", len(transactions))

	assert.Equal(t, transactions[0].Endpoint.Route, "/api/foo")
	assert.Equal(t, transactions[0].Domain, "observability_pipelines_worker.tld")
}

func TestArbitraryTagsHTTPHeader(t *testing.T) {
	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("allow_arbitrary_tags", true)

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "foo"}
	payload := []byte("A payload")
	headers := make(http.Header)

	transactions := forwarder.createHTTPTransactions(endpoint, transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&payload}), transaction.Series, headers)
	require.True(t, len(transactions) > 0)
	assert.Equal(t, "true", transactions[0].Headers.Get(arbitraryTagHTTPHeaderKey))
}

func TestSendHTTPTransactions(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(keysPerDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	endpoint := transaction.Endpoint{Route: "/api/foo", Name: "foo"}
	p1 := []byte("A payload")
	payloads := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p1})
	headers := make(http.Header)
	tr := forwarder.createHTTPTransactions(endpoint, payloads, transaction.Series, headers)

	// fw is stopped, we should get an error
	err = forwarder.sendHTTPTransactions(tr)
	assert.NotNil(t, err)

	forwarder.Start()
	defer forwarder.Stop()
	err = forwarder.sendHTTPTransactions(tr)
	assert.NoError(t, err)
}

func TestSubmitV1Intake(t *testing.T) {
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(monoKeysDomains)
	require.NoError(t, err)
	forwarder := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))
	forwarder.Start()
	defer forwarder.Stop()

	// Overwrite domainForwarders input channel. We are testing that the
	// DefaultForwarder correctly create HTTPTransaction, set the headers
	// and send them to the correct domainForwarder.
	inputQueue := make(chan transaction.Transaction, 1)
	df := forwarder.domainForwarders[testVersionDomain]
	bk := df.highPrio
	df.highPrio = inputQueue
	defer func() { df.highPrio = bk }()

	p := []byte("test")
	assert.Nil(t, forwarder.SubmitV1Intake(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&p}), transaction.Metadata, make(http.Header)))

	select {
	case tr := <-df.highPrio:
		require.NotNil(t, tr)
		httpTr := tr.(*transaction.HTTPTransaction)
		assert.Equal(t, "application/json", httpTr.Headers.Get("Content-Type"))
	case <-time.After(1 * time.Second):
		require.Fail(t, "highPrio queue should contain a transaction")
	}
}

// TestForwarderEndtoEnd is a simple test to see if a payload is well broadcast
// between every components of the forwarder. Corner cases and error are tested
// per component.
func TestForwarderEndtoEnd(t *testing.T) {
	// reseting DroppedOnInput
	highPriorityQueueFull.Set(0)

	var wg sync.WaitGroup
	requests := atomic.NewInt64(0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("%#v\n", r.URL)
		requests.Inc()
		w.WriteHeader(http.StatusOK)
		wg.Done()
	}))
	defer ts.Close()
	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("dd_url", ts.URL)

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1", "api_key2")}, "invalid": {}, "invalid2": nil})
	require.NoError(t, err)
	f := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))

	// when the forwarder is started, the health checker will send 2 requests to check the
	// validity of the two api_keys
	numReqs := int64(2)
	wg.Add(2)

	f.Start()
	defer f.Stop()

	data1 := []byte("data payload 1")
	data2 := []byte("data payload 2")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data1, &data2})
	headers := http.Header{}
	headers.Set("key", "value")

	incRequests := func(num int64) {
		numReqs += num
		wg.Add(int(num))
	}

	// for each call, we send 2 payloads * 2 api_keys
	incRequests(4)
	assert.Nil(t, f.SubmitV1Series(payload, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitSeries(payload, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitV1Intake(payload, transaction.Series, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitV1CheckRuns(payload, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitSketchSeries(payload, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitHostMetadata(payload, headers))

	incRequests(4)
	assert.Nil(t, f.SubmitMetadata(payload, headers))

	// Wait for all the requests to have been received.
	// Timeout after 5 seconds.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// We should receive the following requests:
		// - 9 transactions * 2 payloads per transactions * 2 api_keys
		assert.Equal(t, numReqs, requests.Load())
	case <-time.After(5 * time.Second):
		assert.Fail(t, "timed out waiting for requests")
	}
}

func TestTransactionEventHandlers(t *testing.T) {
	requests := atomic.NewInt64(0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("dd_url", ts.URL)

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	require.NoError(t, err)
	f := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))

	_ = f.Start()
	defer f.Stop()

	data := []byte("data payload 1")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data})
	headers := http.Header{}
	headers.Set("key", "value")

	transactions := f.createHTTPTransactions(endpoints.SeriesEndpoint, payload, transaction.Series, headers)
	require.Len(t, transactions, 1)

	attempts := atomic.NewInt64(0)

	var wg sync.WaitGroup
	wg.Add(1)
	transactions[0].CompletionHandler = func(_ *transaction.HTTPTransaction, statusCode int, _ []byte, _ error) {
		assert.Equal(t, http.StatusOK, statusCode)
		wg.Done()
	}
	transactions[0].AttemptHandler = func(_ *transaction.HTTPTransaction) {
		attempts.Inc()
	}

	err = f.sendHTTPTransactions(transactions)
	require.NoError(t, err)

	wg.Wait()

	assert.Equal(t, int64(1), attempts.Load())
}

func TestTransactionEventHandlersOnRetry(t *testing.T) {
	requests := atomic.NewInt64(0)

	mux := http.NewServeMux()
	mux.HandleFunc(endpoints.V1ValidateEndpoint.Route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(endpoints.SeriesEndpoint.Route, func(w http.ResponseWriter, _ *http.Request) {
		if v := requests.Inc(); v == 1 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("dd_url", ts.URL)

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	require.NoError(t, err)
	f := NewDefaultForwarder(mockConfig, log,
		NewOptionsWithResolvers(mockConfig, log, r),
	)

	_ = f.Start()
	defer f.Stop()

	data := []byte("data payload 1")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data})
	headers := http.Header{}
	headers.Set("key", "value")

	transactions := f.createHTTPTransactions(endpoints.SeriesEndpoint, payload, transaction.Series, headers)
	require.Len(t, transactions, 1)

	attempts := atomic.NewInt64(0)

	var wg sync.WaitGroup
	wg.Add(1)
	transactions[0].CompletionHandler = func(_ *transaction.HTTPTransaction, statusCode int, _ []byte, _ error) {
		assert.Equal(t, http.StatusOK, statusCode)
		wg.Done()
	}
	transactions[0].AttemptHandler = func(_ *transaction.HTTPTransaction) {
		attempts.Inc()
	}

	err = f.sendHTTPTransactions(transactions)
	require.NoError(t, err)

	wg.Wait()

	assert.Equal(t, int64(2), attempts.Load())
}

func TestTransactionEventHandlersNotRetryable(t *testing.T) {
	requests := atomic.NewInt64(0)

	mux := http.NewServeMux()
	mux.HandleFunc(endpoints.V1ValidateEndpoint.Route, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(endpoints.SeriesEndpoint.Route, func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("dd_url", ts.URL)

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	require.NoError(t, err)
	f := NewDefaultForwarder(mockConfig, log,
		NewOptionsWithResolvers(mockConfig, log, r),
	)

	_ = f.Start()
	defer f.Stop()

	data := []byte("data payload 1")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data})
	headers := http.Header{}
	headers.Set("key", "value")

	transactions := f.createHTTPTransactions(endpoints.SeriesEndpoint, payload, transaction.Series, headers)
	require.Len(t, transactions, 1)

	attempts := atomic.NewInt64(0)

	var wg sync.WaitGroup
	wg.Add(1)
	transactions[0].CompletionHandler = func(_ *transaction.HTTPTransaction, statusCode int, _ []byte, _ error) {
		assert.Equal(t, http.StatusInternalServerError, statusCode)
		wg.Done()
	}
	transactions[0].AttemptHandler = func(_ *transaction.HTTPTransaction) {
		attempts.Inc()
	}

	transactions[0].Retryable = false

	err = f.sendHTTPTransactions(transactions)
	require.NoError(t, err)

	wg.Wait()

	assert.Equal(t, int64(1), requests.Load())
	assert.Equal(t, int64(1), attempts.Load())
}

func TestProcessLikePayloadResponseTimeout(t *testing.T) {
	requests := atomic.NewInt64(0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Inc()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	mockConfig := mock.New(t)
	responseTimeout := defaultResponseTimeout

	defaultResponseTimeout = 5 * time.Second
	mockConfig.SetWithoutSource("dd_url", ts.URL)
	mockConfig.SetWithoutSource("forwarder_num_workers", 0) // Set the number of workers to 0 so the txn goes nowhere
	defer func() {
		defaultResponseTimeout = responseTimeout
	}()

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	require.NoError(t, err)
	f := NewDefaultForwarder(mockConfig, log,
		NewOptionsWithResolvers(mockConfig, log, r),
	)

	_ = f.Start()
	defer f.Stop()

	data := []byte("data payload 1")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data})
	headers := http.Header{}
	headers.Set("key", "value")

	transactions := f.createHTTPTransactions(endpoints.SeriesEndpoint, payload, transaction.Series, headers)
	require.Len(t, transactions, 1)

	responses, err := f.submitProcessLikePayload(endpoints.SeriesEndpoint, payload, headers, true)
	require.NoError(t, err)

	_, ok := <-responses
	require.False(t, ok) // channel should have been closed without receiving any responses
}

// Whilst high priority transactions are processed by the worker first,  because the transactions
// are sent in a separate go func, the actual order the get sent will depend on the go scheduler.
// This test ensures that we still on average send high priority transactions before low priority.
func TestHighPriorityTransactionTendency(t *testing.T) {
	var receivedRequests = make(map[string]struct{})
	var mutex sync.Mutex
	var requestChan = make(chan (string), 100)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		bodyStr := string(body)

		// Failed the first time for each request
		if _, found := receivedRequests[bodyStr]; !found {
			receivedRequests[bodyStr] = struct{}{}
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			requestChan <- bodyStr
		}
	}))

	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("forwarder_backoff_max", 0.5)
	mockConfig.SetWithoutSource("forwarder_max_concurrent_requests", 10)

	oldFlushInterval := flushInterval
	flushInterval = 500 * time.Millisecond
	defer func() { flushInterval = oldFlushInterval }()

	log := logmock.New(t)
	r, _ := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	f := NewDefaultForwarder(mockConfig, log,
		NewOptionsWithResolvers(mockConfig, log, r),
	)

	f.Start()
	defer f.Stop()

	headers := http.Header{}
	headers.Set("key", "value")

	for i := range 100 {
		// Every other transaction is high priority
		if i%2 == 0 {
			data := []byte(fmt.Sprintf("high priority %d", i))
			assert.Nil(t, f.SubmitHostMetadata(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data}), headers))
		} else {
			data := []byte(fmt.Sprintf("low priority %d", i))
			assert.Nil(t, f.SubmitMetadata(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data}), headers))
		}

		// Wait so that GetCreatedAt returns a different value for each HTTPTransaction
		time.Sleep(10 * time.Millisecond)
	}

	highPosition := 0
	lowPosition := 0

	for i := range 100 {
		tt := <-requestChan

		if strings.Contains(string(tt), "high") {
			highPosition += i
		} else {
			lowPosition += i
		}
	}

	// Ensure the average position of the high priorities is less than the average position of the lows.
	assert.Greater(t, lowPosition/50, highPosition/50)
}

func TestHighPriorityTransaction(t *testing.T) {
	var receivedRequests = make(map[string]struct{})
	var mutex sync.Mutex
	var requestChan = make(chan (string))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		bodyStr := string(body)

		// Failed the first time for each request
		if _, found := receivedRequests[bodyStr]; !found {
			receivedRequests[bodyStr] = struct{}{}
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
			requestChan <- bodyStr
		}
	}))

	mockConfig := mock.New(t)
	mockConfig.SetWithoutSource("forwarder_backoff_max", 0.5)
	mockConfig.SetWithoutSource("forwarder_max_concurrent_requests", 1)

	oldFlushInterval := flushInterval
	flushInterval = 500 * time.Millisecond
	defer func() { flushInterval = oldFlushInterval }()

	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{ts.URL: {configUtils.NewAPIKeys("path", "api_key1")}})
	require.NoError(t, err)

	f := NewDefaultForwarder(mockConfig, log, NewOptionsWithResolvers(mockConfig, log, r))

	f.Start()
	defer f.Stop()

	data1 := []byte("data payload 1")
	data2 := []byte("data payload 2")
	dataHighPrio := []byte("data payload high Prio")
	headers := http.Header{}
	headers.Set("key", "value")

	assert.Nil(t, f.SubmitMetadata(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data1}), headers))
	// Wait so that GetCreatedAt returns a different value for each HTTPTransaction
	time.Sleep(10 * time.Millisecond)

	// SubmitHostMetadata send the transactions as TransactionPriorityHigh
	assert.Nil(t, f.SubmitHostMetadata(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&dataHighPrio}), headers))
	time.Sleep(10 * time.Millisecond)
	assert.Nil(t, f.SubmitMetadata(transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data2}), headers))

	assert.Equal(t, string(dataHighPrio), <-requestChan)
	assert.Equal(t, string(data2), <-requestChan)
	assert.Equal(t, string(data1), <-requestChan)
}

func TestCustomCompletionHandler(t *testing.T) {
	highPriorityQueueFull.Set(0)

	// Setup a test HTTP server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Point agent configuration to it
	cfg := mock.New(t)
	cfg.SetWithoutSource("dd_url", srv.URL)

	// Now let's create a Forwarder with a custom HTTPCompletionHandler set to it
	done := make(chan struct{})
	defer close(done)
	var handler transaction.HTTPCompletionHandler = func(_ *transaction.HTTPTransaction, _ int, _ []byte, _ error) {
		done <- struct{}{}
	}
	mockConfig := mock.New(t)
	log := logmock.New(t)
	r, err := resolver.NewSingleDomainResolvers(map[string][]configUtils.APIKeys{
		srv.URL: {configUtils.NewAPIKeys("path", "api_key1")},
	})
	require.NoError(t, err)
	options := NewOptionsWithResolvers(mockConfig, log, r)
	options.CompletionHandler = handler

	f := NewDefaultForwarder(mockConfig, log, options)
	f.Start()
	defer f.Stop()

	data := []byte("payload_data")
	payload := transaction.NewBytesPayloadsWithoutMetaData([]*[]byte{&data})
	assert.Nil(t, f.SubmitV1Series(payload, http.Header{}))

	// And finally let's ensure the handler gets called
	var handlerCalled bool
	select {
	case <-done:
		handlerCalled = true
	case <-time.After(time.Second):
	}

	assert.True(t, handlerCalled)
}
