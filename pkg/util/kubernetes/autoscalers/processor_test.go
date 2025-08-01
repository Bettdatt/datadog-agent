// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2017-present Datadog, Inc.

//go:build kubeapiserver

package autoscalers

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	datadogclientmock "github.com/DataDog/datadog-agent/comp/autoscaling/datadogclient/mock"
	telemetryComponent "github.com/DataDog/datadog-agent/comp/core/telemetry"
	"github.com/DataDog/datadog-agent/pkg/clusteragent/autoscaling/custommetrics"
	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	pkgconfigmodel "github.com/DataDog/datadog-agent/pkg/config/model"
	le "github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver/leaderelection/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/pointer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/zorkian/go-datadog-api.v2"
	autoscalingv2 "k8s.io/api/autoscaling/v2beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

const (
	testMaxAge          = 30 * time.Second
	testParallelQueries = 10
)

func makePoints(ts, val int) datadog.DataPoint {
	if ts == 0 {
		ts = (int(metav1.Now().Unix()) - int(testMaxAge.Seconds())) * 1000 // use ms
	}
	tsPtr := float64(ts)
	valPtr := float64(val)
	return datadog.DataPoint{&tsPtr, &valPtr}
}

func makePartialPoints(ts int) datadog.DataPoint {
	tsPtr := float64(ts)
	return datadog.DataPoint{&tsPtr, nil}
}

func TestParallelLimit(t *testing.T) {
	cfg := configmock.New(t)

	cfg.Set("external_metrics_provider.max_parallel_queries", 3000, pkgconfigmodel.SourceLocalConfigProcess)
	p := NewProcessor(nil)
	assert.Equal(t, maxParallelQueries, p.parallelQueries)

	cfg.Set("external_metrics_provider.max_parallel_queries", 0, pkgconfigmodel.SourceLocalConfigProcess)
	p = NewProcessor(nil)
	assert.Equal(t, maxParallelQueries, p.parallelQueries)

	cfg.Set("external_metrics_provider.max_parallel_queries", -20, pkgconfigmodel.SourceLocalConfigProcess)
	p = NewProcessor(nil)
	assert.Equal(t, maxParallelQueries, p.parallelQueries)

	cfg.Set("external_metrics_provider.max_parallel_queries", 20, pkgconfigmodel.SourceLocalConfigProcess)
	p = NewProcessor(nil)
	assert.Equal(t, 20, p.parallelQueries)
}

func TestProcessor_UpdateExternalMetrics(t *testing.T) {
	penTime := (int(time.Now().Unix()) - int(testMaxAge.Seconds()/2)) * 1000
	metricName := "requests_per_s"
	tests := []struct {
		desc     string
		metrics  map[string]custommetrics.ExternalMetricValue
		series   []datadog.Series
		expected map[string]custommetrics.ExternalMetricValue
	}{
		{
			"update invalid metric",
			map[string]custommetrics.ExternalMetricValue{
				"id1": {
					MetricName: metricName,
					Labels:     map[string]string{"foo": "bar"},
					Valid:      false,
				},
			},
			[]datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			map[string]custommetrics.ExternalMetricValue{
				"id1": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"foo": "bar"},
					Value:      14,
					Valid:      true,
				},
			},
		},
		{
			"perform unique from list of input externalMetrics",
			map[string]custommetrics.ExternalMetricValue{
				"id1": {
					MetricName: metricName,
					Labels:     map[string]string{"foo": "bar"},
					Valid:      false,
				},
				"id2": {
					MetricName: metricName,
					Labels:     map[string]string{"foo": "bar"},
					Valid:      false,
				},
			},
			[]datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			map[string]custommetrics.ExternalMetricValue{
				"id1": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"foo": "bar"},
					Value:      14,
					Valid:      true,
				},
				"id2": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"foo": "bar"},
					Value:      14,
					Valid:      true,
				},
			},
		},
		{
			"do not update valid sparse metric",
			map[string]custommetrics.ExternalMetricValue{
				"id2": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"2foo": "bar"},
					Valid:      true,
				},
			},
			[]datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1431492452000, 12),
						makePoints(1431492453000, 14), // Force the point to be considered outdated at all time(> externalMaxAge)
						makePoints(0, 1000),           // Force the point to be considered fresh at all time(< externalMaxAge)
					},
					Scope: pointer.Ptr("2foo:bar"),
				},
			},
			map[string]custommetrics.ExternalMetricValue{
				"id2": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"2foo": "bar"},
					Value:      14,
					Valid:      false,
				},
			},
		},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("#%d %s", i, tt.desc), func(t *testing.T) {
			datadogClientComp := datadogclientmock.New(t).Comp
			datadogClientComp.SetQueryMetricsFunc(func(int64, int64, string) ([]datadog.Series, error) {
				return tt.series, nil
			})
			hpaCl := &Processor{datadogClient: datadogClientComp, externalMaxAge: testMaxAge, parallelQueries: testParallelQueries}

			externalMetrics := hpaCl.UpdateExternalMetrics(tt.metrics)
			fmt.Println(externalMetrics)
			// Timestamps are always set to time.Now() so we cannot assert the value
			// in a unit test.
			strippedTs := make(map[string]custommetrics.ExternalMetricValue)
			for id, m := range externalMetrics {
				m.Timestamp = 0
				strippedTs[id] = m
			}
			fmt.Println(strippedTs)
			for id, m := range tt.expected {
				require.Equal(t, m, strippedTs[id])
			}
		})
	}

	// Test that Datadog not responding yields invaldation.
	emList := map[string]custommetrics.ExternalMetricValue{
		"id1": {
			MetricName: metricName,
			Labels:     map[string]string{"foo": "bar"},
			Valid:      true,
		},
		"id2": {
			MetricName: metricName,
			Labels:     map[string]string{"bar": "baz"},
			Valid:      true,
		},
	}
	datadogClientComp := datadogclientmock.New(t).Comp
	datadogClientComp.SetQueryMetricsFunc(func(int64, int64, string) ([]datadog.Series, error) {
		return nil, fmt.Errorf("API error 400 Bad Request: {\"error\": [\"Rate limit of 300 requests in 3600 seconds reqchec.\"]}")
	})

	hpaCl := &Processor{datadogClient: datadogClientComp, externalMaxAge: testMaxAge, parallelQueries: testParallelQueries}
	invList := hpaCl.UpdateExternalMetrics(emList)
	require.Len(t, invList, len(emList))
	for _, i := range invList {
		require.False(t, i.Valid)
	}
}

var ASCIIRunes = []rune("qwertyuiopasdfghjklzxcvbnm1234567890")

func randStringRune(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = ASCIIRunes[rand.Intn(len(ASCIIRunes))]
	}
	return string(b)
}

func TestValidateExternalMetricsBatching(t *testing.T) {
	metricName := "foo"
	penTime := (int(time.Now().Unix()) - int(testMaxAge.Seconds()/2)) * 1000
	tests := []struct {
		desc       string
		in         []string
		out        []datadog.Series
		batchCalls int
		err        error
		timeout    bool
	}{
		{
			desc: "one batch",
			in: lambdaMakeChunks(14, custommetrics.ExternalMetricValue{
				MetricName: "foo",
				Labels:     map[string]string{"foo": "bar"},
			}),
			out: []datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			batchCalls: 1,
			err:        nil,
			timeout:    false,
		},
		{
			desc: "several batches",
			in: lambdaMakeChunks(158, custommetrics.ExternalMetricValue{
				MetricName: "foo",
				Labels:     map[string]string{"foo": "bar"},
			}),
			out: []datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			batchCalls: 5,
			err:        nil,
			timeout:    false,
		},
		{
			desc: "Overspilling queries",
			in: lambdaMakeChunks(20, custommetrics.ExternalMetricValue{
				MetricName: randStringRune(4000),
				Labels:     map[string]string{"foo": "bar"},
			}),
			out: []datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			batchCalls: 21,
			err:        nil,
			timeout:    false,
		},
		{
			desc: "Overspilling single query",
			in: lambdaMakeChunks(0, custommetrics.ExternalMetricValue{
				MetricName: randStringRune(7000),
				Labels:     map[string]string{"foo": "bar"},
			}),
			out: []datadog.Series{
				{
					Metric: &metricName,
					Points: []datadog.DataPoint{
						makePoints(1531492452000, 12),
						makePoints(penTime, 14), // Force the penultimate point to be considered fresh at all time(< externalMaxAge)
						makePoints(0, 27),
					},
					Scope: pointer.Ptr("foo:bar"),
				},
			},
			batchCalls: 0,
			err:        nil,
			timeout:    false,
		},
	}
	var result struct {
		bc int
		m  sync.Mutex
	}
	res := &result
	for i, tt := range tests {
		res.m.Lock()
		res.bc = 0
		res.m.Unlock()
		t.Run(fmt.Sprintf("#%d %s", i, tt.desc), func(t *testing.T) {
			datadogClientComp := datadogclientmock.New(t).Comp
			datadogClientComp.SetGetRateLimitsFunc(func() map[string]datadog.RateLimit {
				return map[string]datadog.RateLimit{
					queryEndpoint: {
						Limit:     "12",
						Period:    "10",
						Remaining: "200",
						Reset:     "10",
					},
				}
			})
			datadogClientComp.SetQueryMetricsFunc(func(int64, int64, string) ([]datadog.Series, error) {
				res.m.Lock()
				defer res.m.Unlock()
				result.bc++
				if tt.timeout == true && res.bc == 1 {
					// Error will be under the format:
					// Error: Error while executing metric query avg:foo-56{foo:bar}.rollup(30),avg:foo-93{foo:bar}.rollup(30),[...],avg:foo-64{foo:bar}.rollup(30),avg:foo-81{foo:bar}.rollup(30): Networking Error, timeout!!!
					// In the logs, we will be able to see which bundle failed, but for the tests, we can't know which routine will finish first (and therefore have `bc == 1`), so we only check the error returned by the Datadog Servers.
					return nil, fmt.Errorf("networking Error, timeout")
				}
				return tt.out, nil
			})
			p := &Processor{datadogClient: datadogClientComp, parallelQueries: testParallelQueries}

			_ = p.QueryExternalMetric(tt.in, GetDefaultTimeWindow())
			assert.Equal(t, tt.batchCalls, res.bc)
		})
	}
}

func lambdaMakeChunks(numChunks int, chunkToExpand custommetrics.ExternalMetricValue) []string {
	expanded := make([]string, 0, numChunks)
	for i := 0; i <= numChunks; i++ {
		expanded = append(expanded, getKey(fmt.Sprintf("%s-%d", chunkToExpand.MetricName, i), chunkToExpand.Labels, "avg", 30))
	}
	return expanded
}

func TestProcessor_ProcessHPAs(t *testing.T) {
	metricName := "requests_per_s"
	tests := []struct {
		desc     string
		metrics  autoscalingv2.HorizontalPodAutoscaler
		expected map[string]custommetrics.ExternalMetricValue
	}{
		{
			"process valid hpa external metric",
			autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "foo",
				},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					Metrics: []autoscalingv2.MetricSpec{
						{
							Type: autoscalingv2.ExternalMetricSourceType,
							External: &autoscalingv2.ExternalMetricSource{
								MetricName: metricName,
								MetricSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"dcos_version": "1.9.4",
									},
								},
							},
						},
					},
				},
			},
			map[string]custommetrics.ExternalMetricValue{
				"external_metric-default-foo-requests_per_s": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"dcos_version": "1.9.4"},
					Value:      0,
					Valid:      false,
				},
			},
		},
		{
			"process hpa external metrics",
			autoscalingv2.HorizontalPodAutoscaler{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "foo",
				},
				Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
					Metrics: []autoscalingv2.MetricSpec{
						{
							Type: autoscalingv2.ExternalMetricSourceType,
							External: &autoscalingv2.ExternalMetricSource{
								MetricName: "m1",
								MetricSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"dcos_version": "1.9.4",
									},
								},
							},
						},
						{
							Type: autoscalingv2.ExternalMetricSourceType,
							External: &autoscalingv2.ExternalMetricSource{
								MetricName: "m2",
								MetricSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"dcos_version": "2.1.9",
									},
								},
							},
						},
						{
							Type: autoscalingv2.ExternalMetricSourceType,
							External: &autoscalingv2.ExternalMetricSource{
								MetricName: metricName,
								MetricSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"dcos_version": "4.1.1",
									},
								},
							},
						},
					},
				},
			},
			map[string]custommetrics.ExternalMetricValue{
				"external_metric-default-foo-m1": {
					MetricName: "m1",
					Labels:     map[string]string{"dcos_version": "1.9.4"},
					Value:      0,
					Valid:      false,
				},
				"external_metric-default-foo-m2": {
					MetricName: "m2",
					Labels:     map[string]string{"dcos_version": "2.1.9"},
					Value:      0,
					Valid:      false,
				},
				"external_metric-default-foo-m3": {
					MetricName: "requests_per_s",
					Labels:     map[string]string{"dcos_version": "4.1.1"},
					Value:      0, // If Datadog does not even return the metric, store it as invalid with Value = 0
					Valid:      false,
				},
			},
		},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("#%d %s", i, tt.desc), func(t *testing.T) {
			datadogClientComp := datadogclientmock.New(t).Comp
			hpaCl := &Processor{datadogClient: datadogClientComp, externalMaxAge: testMaxAge, parallelQueries: testParallelQueries}

			externalMetrics := hpaCl.ProcessHPAs(&tt.metrics)
			for id, m := range externalMetrics {
				require.True(t, reflect.DeepEqual(m, externalMetrics[id]))
			}
		})
	}
}

// Test that we consistently get the same key.
func TestGetKey(t *testing.T) {
	tests := []struct {
		desc     string
		name     string
		labels   map[string]string
		expected string
	}{
		{
			"correct name and label",
			"kubernetes.io",
			map[string]string{
				"foo": "bar",
			},
			"avg:kubernetes.io{foo:bar}.rollup(30)",
		},
		{
			"correct name and labels",
			"kubernetes.io",
			map[string]string{
				"zfoo": "bar",
				"afoo": "bar",
				"ffoo": "bar",
			},
			"avg:kubernetes.io{afoo:bar,ffoo:bar,zfoo:bar}.rollup(30)",
		},
		{
			"correct name, no labels",
			"kubernetes.io",
			nil,
			"avg:kubernetes.io{*}.rollup(30)",
		},
	}
	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			formatedKey := getKey(test.name, test.labels, "avg", 30)
			require.Equal(t, test.expected, formatedKey)
		})
	}
}

func TestUpdateRateLimiting(t *testing.T) {
	type Results struct {
		Limit     float64
		Period    float64
		Remaining float64
		Reset     float64
	}

	tests := []struct {
		desc       string
		rateLimits map[string]datadog.RateLimit
		results    Results
		error      error
	}{
		{
			desc: "Nominal case",
			rateLimits: map[string]datadog.RateLimit{
				queryEndpoint: {
					Limit:     "12",
					Period:    "3600",
					Reset:     "11",
					Remaining: "120",
				},
			},
			results: Results{
				Limit:     12,
				Period:    3600,
				Reset:     11,
				Remaining: 120,
			},
			error: nil,
		},
		{
			desc: "Missing header case",
			rateLimits: map[string]datadog.RateLimit{
				queryEndpoint: {
					Limit:  "12",
					Period: "3600",
					Reset:  "11",
				},
			},
			results: Results{
				Limit:  12,
				Period: 3600,
				Reset:  11,
			},
			error: fmt.Errorf("strconv.Atoi: parsing \"\": invalid syntax"),
		},
		{
			desc: "Missing headers case",
			rateLimits: map[string]datadog.RateLimit{
				queryEndpoint: {
					Limit:  "12",
					Period: "3600",
				},
			},
			results: Results{
				Limit:  12,
				Period: 3600,
			},
			// Although several headers are missing, the Aggregate will only return 1 error as they are the same
			error: fmt.Errorf("strconv.Atoi: parsing \"\": invalid syntax"),
		},
	}

	rateLimitsRemaining = &mockGauge{values: make(map[string]float64)}
	rateLimitsPeriod = &mockGauge{values: make(map[string]float64)}
	rateLimitsLimit = &mockGauge{values: make(map[string]float64)}
	rateLimitsReset = &mockGauge{values: make(map[string]float64)}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("#%d %s", i, tt.desc), func(t *testing.T) {
			datadogClientComp := datadogclientmock.New(t).Comp
			datadogClientComp.SetGetRateLimitsFunc(func() map[string]datadog.RateLimit {
				return tt.rateLimits
			})
			hpaCl := &Processor{datadogClient: datadogClientComp, externalMaxAge: testMaxAge, parallelQueries: testParallelQueries}

			hpaCl.updateRateLimitingMetrics()
			key := strings.Join([]string{queryEndpoint, le.JoinLeaderValue}, ",")
			assert.Equal(t, rateLimitsLimit.(*mockGauge).values[key], tt.results.Limit)
			assert.Equal(t, rateLimitsReset.(*mockGauge).values[key], tt.results.Reset)
			assert.Equal(t, rateLimitsPeriod.(*mockGauge).values[key], tt.results.Period)
			assert.Equal(t, rateLimitsRemaining.(*mockGauge).values[key], tt.results.Remaining)
		})
		resetCounters(queryEndpoint)
	}
}

func resetCounters(endpoint string) {
	rateLimitsRemaining.Set(0, endpoint, "true")
	rateLimitsPeriod.Set(0, endpoint, "true")
	rateLimitsLimit.Set(0, endpoint, "true")
	rateLimitsReset.Set(0, endpoint, "true")
}

type mockGauge struct {
	values map[string]float64
}

// Set stores the value for the given tags.
func (m *mockGauge) Set(value float64, tagsValue ...string) {
	m.values[strings.Join(tagsValue, ",")] = value
}

// Inc increments the Gauge value.
func (m *mockGauge) Inc(tagsValue ...string) {
	m.values[strings.Join(tagsValue, ",")] += 1.0
}

// Dec decrements the Gauge value.
func (m *mockGauge) Dec(tagsValue ...string) {
	m.values[strings.Join(tagsValue, ",")] -= 1.0
}

// Add adds the value to the Gauge value.
func (m *mockGauge) Add(value float64, tagsValue ...string) {
	m.values[strings.Join(tagsValue, ",")] += value
}

// Sub subtracts the value to the Gauge value.
func (m *mockGauge) Sub(value float64, tagsValue ...string) {
	m.values[strings.Join(tagsValue, ",")] -= value
}

// Delete deletes the value for the Gauge with the given tags.
func (m *mockGauge) Delete(tagsValue ...string) {
	delete(m.values, strings.Join(tagsValue, ","))
}

// DeletePartialMatch does nothing
func (m *mockGauge) DeletePartialMatch(_ map[string]string) {}

func (*mockGauge) WithValues(_ ...string) telemetryComponent.SimpleGauge       { return nil }
func (*mockGauge) WithTags(_ map[string]string) telemetryComponent.SimpleGauge { return nil }
