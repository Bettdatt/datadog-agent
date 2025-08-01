// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubelet

package kubelet

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/DataDog/datadog-agent/pkg/config/create"
	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/errors"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	pkglogsetup "github.com/DataDog/datadog-agent/pkg/util/log/setup"
)

const (
	fakePath = "./testdata/invalidTokenFilePath"
)

// dummyKubelet allows tests to mock a kubelet's responses
type dummyKubelet struct {
	sync.Mutex
	Requests  chan *http.Request
	PodsBody  []byte
	StatsBody []byte

	testingCertificate string
	testingPrivateKey  string
}

func newDummyKubelet(podListJSONPath string, statsSummaryJSONPath string) (*dummyKubelet, error) {
	kubelet := &dummyKubelet{Requests: make(chan *http.Request, 3)}
	if podListJSONPath != "" {
		err := kubelet.loadPodList(podListJSONPath)
		if err != nil {
			return nil, err
		}
	}
	if statsSummaryJSONPath != "" {
		err := kubelet.loadStatsSummary(statsSummaryJSONPath)
		if err != nil {
			return nil, err
		}
	}
	return kubelet, nil
}

func (d *dummyKubelet) loadPodList(podListJSONPath string) error {
	d.Lock()
	defer d.Unlock()
	podList, err := os.ReadFile(podListJSONPath)
	if err != nil {
		return err
	}
	d.PodsBody = podList
	return nil
}

func (d *dummyKubelet) loadStatsSummary(statsSummaryJSONPath string) error {
	d.Lock()
	defer d.Unlock()
	stats, err := os.ReadFile(statsSummaryJSONPath)
	if err != nil {
		return err
	}
	d.StatsBody = stats
	return nil
}

func (d *dummyKubelet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.Lock()
	defer d.Unlock()
	log.Debugf("dummyKubelet received %s on %s", r.Method, r.URL.Path)
	d.Requests <- r
	switch r.URL.Path {
	case "/healthz":
		w.Write([]byte("ok"))

	case "/pods":
		if d.PodsBody == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s, err := w.Write(d.PodsBody)
		log.Debugf("dummyKubelet wrote %d bytes, err: %v", s, err)

	case "/stats/summary":
		if d.StatsBody == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s, err := w.Write(d.StatsBody)
		log.Debugf("dummyKubelet wrote %d bytes for /stats/summary, err: %v", s, err)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (d *dummyKubelet) parsePort(ts *httptest.Server) (*httptest.Server, int, error) {
	kubeletURL, err := url.Parse(ts.URL)
	if err != nil {
		return nil, 0, err
	}
	kubeletPort, err := strconv.Atoi(kubeletURL.Port())
	if err != nil {
		return nil, 0, err
	}
	log.Debugf("Starting on port %d", kubeletPort)
	return ts, kubeletPort, nil
}

func (d *dummyKubelet) dropRequests() {
	for {
		select {
		case <-d.Requests:
			continue
		default:
			return
		}
	}
}

func pemBlockForKey(privateKey interface{}) (*pem.Block, error) {
	switch k := privateKey.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}, nil

	default:
		return nil, fmt.Errorf("unrecognized format for privateKey")
	}
}

func (d *dummyKubelet) StartTLS() (*httptest.Server, int, error) {
	ts := httptest.NewTLSServer(d)
	cert := ts.TLS.Certificates
	if len(ts.TLS.Certificates) != 1 {
		return ts, 0, fmt.Errorf("unexpected number of testing certificates: 1 != %d", len(ts.TLS.Certificates))
	}
	certOut, err := os.CreateTemp("", "kubelet-test-cert-")
	d.testingCertificate = certOut.Name()
	if err != nil {
		return ts, 0, err
	}
	keyOut, err := os.CreateTemp("", "kubelet-test-key-")
	d.testingPrivateKey = keyOut.Name()
	if err != nil {
		return ts, 0, err
	}
	for _, c := range cert {
		for _, s := range c.Certificate {
			pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: s})
			certOut.Close()
		}
		p, err := pemBlockForKey(c.PrivateKey)
		if err != nil {
			return ts, 0, err
		}
		err = pem.Encode(keyOut, p)
		if err != nil {
			return ts, 0, err
		}
	}
	return d.parsePort(ts)
}

func (d *dummyKubelet) Start() (*httptest.Server, int, error) {
	ts := httptest.NewServer(d)
	return d.parsePort(ts)
}

type KubeletTestSuite struct {
	suite.Suite
}

func (suite *KubeletTestSuite) getCustomKubeUtil() KubeUtilInterface {
	suite.T().Helper()

	kubeutil, err := GetKubeUtil()
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), kubeutil)

	return kubeutil
}

// Make sure globalKubeUtil is deleted before each test
func (suite *KubeletTestSuite) SetupTest() {
	ResetGlobalKubeUtil()
	ResetCache()

	jsoniter.RegisterTypeDecoder("kubelet.PodList", nil)
}

func (suite *KubeletTestSuite) TestLocateKubeletHTTP() {
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	ku := NewKubeUtil()
	err = ku.init()
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), ku)

	select {
	case r := <-kubelet.Requests:
		require.Equal(suite.T(), "GET", r.Method)
		require.Equal(suite.T(), "/spec", r.URL.Path)
	case <-time.After(2 * time.Second):
		require.FailNow(suite.T(), "Timeout on receive channel")
	}

	require.EqualValues(suite.T(),
		map[string]string{
			"url": fmt.Sprintf("http://127.0.0.1:%d", kubeletPort),
		}, ku.GetRawConnectionInfo())
}

func (suite *KubeletTestSuite) TestGetLocalPodList() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.GetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)
	require.Len(suite.T(), pods, 7)

	select {
	case r := <-kubelet.Requests:
		require.Equal(suite.T(), r.Method, "GET")
		require.Equal(suite.T(), r.URL.Path, "/pods")
	case <-time.After(2 * time.Second):
		require.FailNow(suite.T(), "Timeout on receive channel")
	}
}

func (suite *KubeletTestSuite) TestGetLocalPodListWithBrokenKubelet() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/invalid.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.GetLocalPodList(ctx)
	require.NotNil(suite.T(), err)
	require.Len(suite.T(), pods, 0)
	require.True(suite.T(), errors.IsRetriable(err))
}

func (suite *KubeletTestSuite) TestGetNodenameStatsSummary() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("", "./testdata/stats_summary.json")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")
	mockConfig.SetWithoutSource("kubelet_use_api_server", true)

	os.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	os.Setenv("KUBERNETES_SERVICE_PORT_HTTPS", "443")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	// Nodename should already be set from `init()`
	require.Equal(suite.T(), "my-node-name", kubeutil.(*KubeUtil).kubeletClient.config.nodeName)

	hostname, err := kubeutil.GetNodename(ctx)
	require.Nil(suite.T(), err)
	require.Equal(suite.T(), "my-node-name", hostname)

	select {
	case r := <-kubelet.Requests:
		require.FailNow(suite.T(), "Expected no new requests, but got: "+r.URL.Path)
	default:
	}

	os.Unsetenv("KUBERNETES_SERVICE_HOST")
	os.Unsetenv("KUBERNETES_SERVICE_PORT")
}

func (suite *KubeletTestSuite) TestGetNodename() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	hostname, err := kubeutil.GetNodename(ctx)
	require.Nil(suite.T(), err)
	require.Equal(suite.T(), "my-node-name", hostname)

	select {
	case r := <-kubelet.Requests:
		require.Equal(suite.T(), r.Method, "GET")
		require.Equal(suite.T(), r.URL.Path, "/pods")
	case <-time.After(2 * time.Second):
		require.FailNow(suite.T(), "Timeout on receive channel")
	}
}

func (suite *KubeletTestSuite) TestPodlistCache() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	kubeutil.GetLocalPodList(ctx)
	r := <-kubelet.Requests
	require.Equal(suite.T(), "/pods", r.URL.Path)

	// The request should be cached now
	_, err = kubeutil.GetLocalPodList(ctx)
	require.Nil(suite.T(), err)

	select {
	case <-kubelet.Requests:
		assert.FailNow(suite.T(), "podlist request should have been cached")
	default:
		// Cache working as expected
	}

	// test successful cache wipe
	ResetCache()
	_, err = kubeutil.GetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	r = <-kubelet.Requests
	require.Equal(suite.T(), "/pods", r.URL.Path)
}

func (suite *KubeletTestSuite) TestKubeletInitFailOnToken() {
	mockConfig := configmock.New(suite.T())

	// without token, with certs on HTTPS insecure
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.StartTLS()
	defer os.Remove(k.testingCertificate)
	defer os.Remove(k.testingPrivateKey)
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", fakePath)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")

	ku := NewKubeUtil()
	err = ku.init()

	expectedErr := fmt.Errorf("could not read token from %s: open %s: no such file or directory", fakePath, fakePath)
	if runtime.GOOS == "windows" {
		expectedErr = fmt.Errorf("could not read token from %s: open %s: The system cannot find the file specified", fakePath, fakePath)
	}
	assert.Contains(suite.T(), err.Error(), expectedErr.Error())
	assert.Nil(suite.T(), ku.kubeletClient)
}

func (suite *KubeletTestSuite) TestKubeletInitTokenHttps() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	// with a token, without certs on HTTPS insecure
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.StartTLS()
	defer os.Remove(k.testingCertificate)
	defer os.Remove(k.testingPrivateKey)
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "./testdata/fakeBearerToken")
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")
	mockConfig.SetWithoutSource("kubelet_client_ca", "./testdata/ca.crt")

	ku := NewKubeUtil()
	err = ku.init()
	require.Nil(suite.T(), err)
	<-k.Requests // Throwing away first GET

	assert.Equal(suite.T(), fmt.Sprintf("https://127.0.0.1:%d", kubeletPort), ku.kubeletClient.kubeletURL)
	b, code, err := ku.QueryKubelet(ctx, "/healthz")
	assert.Nil(suite.T(), err)
	assert.Equal(suite.T(), "ok", string(b))
	assert.Equal(suite.T(), 200, code)
	r := <-k.Requests
	assert.Equal(suite.T(), "Bearer fakeBearerToken", r.Header.Get(authorizationHeaderKey))

	require.EqualValues(suite.T(),
		map[string]string{
			"url":        fmt.Sprintf("https://127.0.0.1:%d", kubeletPort),
			"verify_tls": "false",
			"ca_cert":    "./testdata/ca.crt",
			"token":      "fakeBearerToken",
		}, ku.GetRawConnectionInfo())
}

func (suite *KubeletTestSuite) TestKubeletInitHttpsCerts() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	// with a token, without certs on HTTPS insecure
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.StartTLS()
	defer os.Remove(k.testingCertificate)
	defer os.Remove(k.testingPrivateKey)
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "./testdata/fakeBearerToken")
	mockConfig.SetWithoutSource("kubelet_tls_verify", true)
	mockConfig.SetWithoutSource("kubelet_client_crt", k.testingCertificate)
	mockConfig.SetWithoutSource("kubelet_client_key", k.testingPrivateKey)
	mockConfig.SetWithoutSource("kubelet_client_ca", k.testingCertificate)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")

	ku := NewKubeUtil()
	err = ku.init()
	require.Nil(suite.T(), err)
	<-k.Requests // Throwing away first GET

	assert.Equal(suite.T(), fmt.Sprintf("https://127.0.0.1:%d", kubeletPort), ku.kubeletClient.kubeletURL)
	if transport, ok := ku.kubeletClient.client.Transport.(*http.Transport); ok {
		assert.False(suite.T(), transport.TLSClientConfig.InsecureSkipVerify)
	}
	b, code, err := ku.QueryKubelet(ctx, "/healthz")
	assert.Nil(suite.T(), err)
	assert.Equal(suite.T(), "ok", string(b))
	assert.Equal(suite.T(), 200, code)
	r := <-k.Requests
	assert.Equal(suite.T(), "Bearer fakeBearerToken", r.Header.Get(authorizationHeaderKey))
	if transport, ok := ku.kubeletClient.client.Transport.(*http.Transport); ok {
		clientCerts := transport.TLSClientConfig.Certificates
		require.Equal(suite.T(), 1, len(clientCerts))
		assert.Equal(suite.T(), clientCerts, s.TLS.Certificates)
	}

	require.EqualValues(suite.T(),
		map[string]string{
			"url":        fmt.Sprintf("https://127.0.0.1:%d", kubeletPort),
			"verify_tls": "true",
			"client_crt": k.testingCertificate,
			"client_key": k.testingPrivateKey,
			"ca_cert":    k.testingCertificate,
			"token":      "fakeBearerToken",
		}, ku.GetRawConnectionInfo())
}

func (suite *KubeletTestSuite) TestKubeletInitTokenHttp() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	// with an unused token, without certs on HTTP
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.Start()
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "./testdata/unusedBearerToken")
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")

	ku := NewKubeUtil()
	err = ku.init()
	require.Nil(suite.T(), err)
	assert.Equal(suite.T(), fmt.Sprintf("http://127.0.0.1:%d", kubeletPort), ku.kubeletClient.kubeletURL)
	assert.True(suite.T(), ku.kubeletClient.client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify)
	b, code, err := ku.QueryKubelet(ctx, "/healthz")
	assert.Nil(suite.T(), err)
	assert.Equal(suite.T(), "ok", string(b))
	assert.Equal(suite.T(), 200, code)
	assert.Equal(suite.T(), 0, len(ku.kubeletClient.client.Transport.(*http.Transport).TLSClientConfig.Certificates))

	require.EqualValues(suite.T(),
		map[string]string{
			"url": fmt.Sprintf("http://127.0.0.1:%d", kubeletPort),
			// token must be unset
		}, ku.GetRawConnectionInfo())
}

func (suite *KubeletTestSuite) TestKubeletInitHttp() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	// without token, without certs on HTTP
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.Start()
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")

	ku := NewKubeUtil()
	err = ku.init()
	require.Nil(suite.T(), err)
	assert.Equal(suite.T(), fmt.Sprintf("http://127.0.0.1:%d", kubeletPort), ku.kubeletClient.kubeletURL)
	assert.True(suite.T(), ku.kubeletClient.client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify)
	b, code, err := ku.QueryKubelet(ctx, "/healthz")
	assert.Nil(suite.T(), err)
	assert.Equal(suite.T(), "ok", string(b))
	assert.Equal(suite.T(), 200, code)
	assert.Equal(suite.T(), 0, len(ku.kubeletClient.client.Transport.(*http.Transport).TLSClientConfig.Certificates))

	require.EqualValues(suite.T(),
		map[string]string{
			"url": fmt.Sprintf("http://127.0.0.1:%d", kubeletPort),
		}, ku.GetRawConnectionInfo())
}

func (suite *KubeletTestSuite) TestGetKubeletHostFromConfig() {
	mockConfig := configmock.New(suite.T())

	// without token, without certs on HTTP
	k, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)

	s, kubeletPort, err := k.Start()
	require.Nil(suite.T(), err)
	defer s.Close()

	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "127.0.0.1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, hostnames := getKubeletHostFromConfig(ctx, mockConfig.GetString("kubernetes_kubelet_host"))
	assert.Equal(suite.T(), ips, []string{"127.0.0.1"})
	// 127.0.0.1 is aliased to kubernetes.docker.internal by Docker for Windows
	assert.Condition(suite.T(), func() bool {
		// On Windows (AppVeyor), "127.0.0.1" resolves to nothing
		if runtime.GOOS == "windows" {
			return true
		}
		if len(hostnames) > 0 {
			return hostnames[0] == "localhost" || hostnames[0] == "kubernetes.docker.internal."
		}
		return false
	})

	// when kubernetes_kubelet_host is not set
	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "")
	ips, hostnames = getKubeletHostFromConfig(ctx, mockConfig.GetString("kubernetes_kubelet_host"))
	assert.Equal(suite.T(), ips, []string(nil))
	assert.Equal(suite.T(), hostnames, []string(nil))
}

func (suite *KubeletTestSuite) TestPodListNoExpire() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())
	mockConfig.SetWithoutSource("kubernetes_pod_expiration_duration", 0)

	kubelet, err := newDummyKubelet("./testdata/podlist_expired.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil, err := GetKubeUtil()
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), kubeutil)
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)
	require.Len(suite.T(), pods.Items, 4)

	assert.Equal(suite.T(), pods.ExpiredCount, 0)
}

func (suite *KubeletTestSuite) TestPodListExpire() {
	// Fixtures contains four pods:
	//   - dd-agent-ntepl old but running
	//   - hello1-1550504220-ljnzx succeeded and old enough to expire
	//   - hello5-1550509440-rlgvf succeeded but not old enough
	//   - hello8-1550505780-kdnjx has one old container and a recent container, don't expire

	ctx := context.Background()
	mockConfig := configmock.New(suite.T())
	mockConfig.SetWithoutSource("kubernetes_pod_expiration_duration", 15*60)

	kubelet, err := newDummyKubelet("./testdata/podlist_expired.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	// Mock time.Now call
	kubeutil.(*KubeUtil).podUnmarshaller.timeNowFunction = func() time.Time {
		t, _ := time.Parse(time.RFC3339, "2019-02-18T16:00:06Z")
		return t
	}

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)
	require.Len(suite.T(), pods.Items, 3)

	assert.Equal(suite.T(), pods.ExpiredCount, 1)

	// Test we kept the right pods
	expectedNames := []string{"dd-agent-ntepl", "hello5-1550509440-rlgvf", "hello8-1550505780-kdnjx"}
	var podNames []string
	for _, p := range pods.Items {
		podNames = append(podNames, p.Metadata.Name)
	}
	assert.Equal(suite.T(), expectedNames, podNames)
}

func TestKubeletTestSuite(t *testing.T) {
	// NOTE: This test suite fails using configmock.New(t), TODO: investigate and fix this
	cfg := create.NewConfig("test")
	pkglogsetup.SetupLogger(
		pkglogsetup.LoggerName("test"),
		"trace",
		"",
		"",
		false,
		true,
		false,
		cfg,
	)
	suite.Run(t, new(KubeletTestSuite))
}

func (suite *KubeletTestSuite) TestContainerEnvVars() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_1.8-2.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)

	var nginxPod *Pod
	for _, pod := range pods.Items {
		if pod.Metadata.Name == "nginx-99d8b564-4r4vq" {
			nginxPod = pod
			break
		}
	}
	require.NotNil(suite.T(), nginxPod)

	var nginxContainer *ContainerSpec
	for _, container := range nginxPod.Spec.Containers {
		if container.Name == "nginx" {
			nginxContainer = &container
			break
		}
	}
	require.NotNil(suite.T(), nginxContainer)

	expectedEnvVars := []EnvVar{
		// Variable explicitly defined in the pod spec
		{Name: "DEFINED_VAR", Value: "true"},
		// Variable from an external source (eg. ConfigMap, Secret, Downward API, etc.)
		{Name: "UNDEFINED_VAR", Value: "", ValueFrom: &struct{}{}},
	}
	assert.ElementsMatch(suite.T(), nginxContainer.Env, expectedEnvVars)
}

func (suite *KubeletTestSuite) TestPodListWithNullPod() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_null_pod.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)
	require.Len(suite.T(), pods.Items, 1)

	for _, po := range pods.Items {
		require.NotNil(suite.T(), po)
	}
}

func (suite *KubeletTestSuite) TestPodListOnKubeletInit() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_startup.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.NotNil(suite.T(), err)
	require.Nil(suite.T(), pods)
}

func (suite *KubeletTestSuite) TestPodListWithPersistentVolumeClaim() {
	ctx := context.Background()
	mockConfig := configmock.New(suite.T())

	kubelet, err := newDummyKubelet("./testdata/podlist_persistent_volume_claim.json", "")
	require.Nil(suite.T(), err)
	ts, kubeletPort, err := kubelet.Start()
	require.Nil(suite.T(), err)
	defer ts.Close()

	mockConfig.SetWithoutSource("kubernetes_kubelet_host", "localhost")
	mockConfig.SetWithoutSource("kubernetes_http_kubelet_port", kubeletPort)
	mockConfig.SetWithoutSource("kubernetes_https_kubelet_port", -1)
	mockConfig.SetWithoutSource("kubelet_tls_verify", false)
	mockConfig.SetWithoutSource("kubelet_auth_token_path", "")

	kubeutil := suite.getCustomKubeUtil()
	kubelet.dropRequests() // Throwing away first GETs

	pods, err := kubeutil.ForceGetLocalPodList(ctx)
	require.Nil(suite.T(), err)
	require.NotNil(suite.T(), pods)
	require.Len(suite.T(), pods.Items, 9)

	found := false
	for _, po := range pods.Items {
		if po.Metadata.Name == "cassandra-0" {
			found = po.Spec.Volumes[0].PersistentVolumeClaim.ClaimName == "cassandra-data-cassandra-0"
			break
		}
	}

	require.True(suite.T(), found)
}
