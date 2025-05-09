// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver

//nolint:revive // TODO(CAPP) Fix revive linter
package orchestrator

import (
	"context"
	"embed"
	"encoding/json"
	"expvar"
	"fmt"
	"io"

	"github.com/DataDog/datadog-agent/comp/core/status"
	"github.com/DataDog/datadog-agent/pkg/clusteragent/clusterchecks"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/orchestrator"
	orchcfg "github.com/DataDog/datadog-agent/pkg/orchestrator/config"
	pkgorchestratormodel "github.com/DataDog/datadog-agent/pkg/orchestrator/model"
	"github.com/DataDog/datadog-agent/pkg/util/hostname"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver/common"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver/leaderelection"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/clustername"

	"k8s.io/client-go/kubernetes"
)

type stats struct {
	orchestrator.CheckStats
	NodeType  string
	TotalHits int64
	TotalMiss int64
}

// GetStatus returns status info for the orchestrator explorer.
func GetStatus(ctx context.Context, apiCl kubernetes.Interface) map[string]interface{} {
	status := make(map[string]interface{})
	if !pkgconfigsetup.Datadog().GetBool("orchestrator_explorer.enabled") {
		status["Disabled"] = "The orchestrator explorer is not enabled on the Cluster Agent"
		return status
	}

	if !pkgconfigsetup.Datadog().GetBool("leader_election") {
		status["Disabled"] = "Leader election is not enabled on the Cluster Agent. The orchestrator explorer needs leader election for resource collection."
		return status
	}

	// get cluster uid
	clusterID, err := common.GetOrCreateClusterID(apiCl.CoreV1())
	if err != nil {
		status["ClusterIDError"] = err.Error()
	} else {
		status["ClusterID"] = clusterID
	}

	setClusterName(ctx, status)

	// get orchestrator endpoints
	endpoints := map[string][]string{}
	orchestratorCfg := orchcfg.NewDefaultOrchestratorConfig(nil)
	err = orchestratorCfg.Load()
	if err == nil {
		// obfuscate the api keys
		for _, endpoint := range orchestratorCfg.OrchestratorEndpoints {
			endpointStr := endpoint.Endpoint.String()
			if len(endpoint.APIKey) > 5 {
				endpoints[endpointStr] = append(endpoints[endpointStr], endpoint.APIKey[len(endpoint.APIKey)-5:])
			}
		}
	}
	status["OrchestratorEndpoints"] = endpoints
	setCacheInformationDCAMode(status)
	setCollectionIsWorkingDCAMode(status)
	setManifestBufferInformationDCAMode(status)
	setSkippedResourcesInformationDCAMode(status)

	// rewriting DCA Mode in case we are running in cluster check mode.
	if orchestrator.KubernetesResourceCache.ItemCount() == 0 && pkgconfigsetup.Datadog().GetBool("cluster_checks.enabled") {
		// we need to check first whether we have dispatched checks to CLC
		stats, err := clusterchecks.GetStats()
		if err != nil {
			status["CLCError"] = err.Error()
		} else {
			// this and the cache section will only be shown on the DCA leader
			if !stats.Active {
				status["CLCEnabled"] = true
				status["CollectionWorking"] = "Clusterchecks are activated but still warming up, the collection could be running on CLC Runners. To verify that we need the clusterchecks to be warmed up."
			} else {
				if _, ok := stats.CheckNames[orchestrator.CheckName]; ok {
					status["CLCEnabled"] = true
					status["CacheNumber"] = "No Elements in the cache, since collection is run on CLC Runners"
					status["CollectionWorking"] = "The collection is not running on the DCA but on the CLC Runners"
				}
			}
		}
	}

	// get options
	if pkgconfigsetup.Datadog().GetBool("orchestrator_explorer.container_scrubbing.enabled") {
		status["ContainerScrubbing"] = "Container scrubbing: enabled"
	}

	if pkgconfigsetup.Datadog().GetBool("orchestrator_explorer.manifest_collection.enabled") {
		status["ManifestCollection"] = "Manifest collection: enabled"
	}

	return status
}

func setCacheInformationDCAMode(status map[string]interface{}) {

	// get cache size
	status["CacheNumber"] = orchestrator.KubernetesResourceCache.ItemCount()

	// get cache hits
	cacheHitsJSON := []byte(expvar.Get("orchestrator-cache").String())
	cacheHits := make(map[string]int64)
	json.Unmarshal(cacheHitsJSON, &cacheHits) //nolint:errcheck
	status["CacheHits"] = cacheHits

	// get cache Miss
	cacheMissJSON := []byte(expvar.Get("orchestrator-sends").String())
	cacheMiss := make(map[string]int64)
	json.Unmarshal(cacheMissJSON, &cacheMiss) //nolint:errcheck
	status["CacheMiss"] = cacheMiss
	cacheStats := make(map[string]stats)

	// get cache efficiency
	for _, node := range pkgorchestratormodel.NodeTypes() {
		if value, found := orchestrator.KubernetesResourceCache.Get(orchestrator.BuildStatsKey(node)); found {
			orcStats := value.(orchestrator.CheckStats)
			totalMiss := cacheMiss[orcStats.String()]
			totalHit := cacheHits[orcStats.String()]
			s := stats{
				CheckStats: orcStats,
				NodeType:   orcStats.String(),
				TotalHits:  totalHit,
				TotalMiss:  totalMiss,
			}
			cacheStats[node.String()+"sStats"] = s
		}
	}
	status["CacheInformation"] = cacheStats
}

func setClusterName(ctx context.Context, status map[string]interface{}) {
	errorMsg := "No cluster name was detected. This means resource collection will not work."

	hname, err := hostname.Get(ctx)
	if err != nil {
		status["ClusterNameError"] = fmt.Sprintf("Error detecting cluster name: %s.\n%s", err.Error(), errorMsg)
	} else {
		if cName := clustername.GetClusterName(ctx, hname); cName != "" {
			status["ClusterName"] = cName
		} else {
			status["ClusterName"] = errorMsg
		}
	}
}

// setCollectionIsWorkingDCAMode checks whether collection is running by checking telemetry/cache data
func setCollectionIsWorkingDCAMode(status map[string]interface{}) {
	engine, err := leaderelection.GetLeaderEngine()
	if err != nil {
		status["CollectionWorking"] = "The collection has not run successfully because no leader has been elected."
		status["LeaderError"] = err
		return
	}
	status["Leader"] = engine.IsLeader()
	status["LeaderName"] = engine.GetLeader()
	if engine.IsLeader() {
		c := orchestrator.KubernetesResourceCache.ItemCount()
		if c > 0 {
			status["CollectionWorking"] = "The collection is at least partially running since the cache has been populated."
		} else {
			status["CollectionWorking"] = "The collection has not run successfully yet since the cache is empty."
		}
	} else {
		status["CollectionWorking"] = "The collection is not running because this agent is not the leader"
	}

}

func setManifestBufferInformationDCAMode(status map[string]interface{}) {
	manifestBufferJSON := []byte(expvar.Get("orchestrator-manifest-buffer").String())
	manifestBuffer := make(map[string]int64)
	json.Unmarshal(manifestBufferJSON, &manifestBuffer) //nolint:errcheck
	status["ManifestsFlushedLastTime"] = manifestBuffer["ManifestsFlushedLastTime"]
	status["BufferFlushed"] = manifestBuffer["BufferFlushed"]
	delete(manifestBuffer, "ManifestsFlushedLastTime")
	delete(manifestBuffer, "BufferFlushed")
	status["ManifestBuffer"] = manifestBuffer

}

func setSkippedResourcesInformationDCAMode(status map[string]interface{}) {
	skippedResourcesJSON := []byte(expvar.Get("orchestrator-skipped-resources").String())
	skippedResources := make(map[string]string)
	err := json.Unmarshal(skippedResourcesJSON, &skippedResources)
	if err != nil {
		return
	}

	skippedResourcesFiltered := make(map[string]string)
	for informerName, reason := range skippedResources {
		if reason != "" {
			skippedResourcesFiltered[informerName] = reason
		}
	}
	status["SkippedResources"] = skippedResourcesFiltered
}

// Provider provides the functionality to populate the status output
type Provider struct{}

//go:embed status_templates
var templatesFS embed.FS

// Name returns the name
func (Provider) Name() string {
	return "Orchestrator Explorer"
}

// Section return the section
func (Provider) Section() string {
	return "Orchestrator Explorer"
}

// JSON populates the status map
func (Provider) JSON(_ bool, stats map[string]interface{}) error {
	populateStatus(stats)

	return nil
}

// Text renders the text output
func (Provider) Text(_ bool, buffer io.Writer) error {
	return status.RenderText(templatesFS, "orchestrator.tmpl", buffer, getStatusInfo())
}

// HTML renders the html output
func (Provider) HTML(_ bool, _ io.Writer) error {
	return nil
}

func populateStatus(stats map[string]interface{}) {
	apiCl, apiErr := apiserver.GetAPIClient()

	if pkgconfigsetup.Datadog().GetBool("orchestrator_explorer.enabled") {
		if apiErr != nil {
			stats["orchestrator"] = map[string]string{"Error": apiErr.Error()}
		} else {
			orchestratorStats := GetStatus(context.TODO(), apiCl.Cl)
			stats["orchestrator"] = orchestratorStats
		}
	}
}

func getStatusInfo() map[string]interface{} {
	stats := make(map[string]interface{})

	populateStatus(stats)

	return stats
}
