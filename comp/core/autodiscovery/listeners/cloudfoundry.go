// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2017-present Datadog, Inc.

//go:build clusterchecks

package listeners

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	filter "github.com/DataDog/datadog-agent/comp/core/workloadfilter/def"
	"github.com/DataDog/datadog-agent/pkg/util/cloudproviders/cloudfoundry"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// exported for testing purposes
const (
	CfServiceContainerIP = "container-ip"
)

// CloudFoundryListener defines a listener that periodically fetches Cloud Foundry services from the BBS API
type CloudFoundryListener struct {
	sync.RWMutex
	newService    chan<- Service
	delService    chan<- Service
	services      map[string]Service // maps ADIdentifiers to services
	stop          chan bool
	refreshCount  int64
	refreshTicker *time.Ticker
	bbsCache      cloudfoundry.BBSCacheI
}

// CloudFoundryService defines a Cloud Foundry service
type CloudFoundryService struct {
	tags           []string
	adIdentifier   cloudfoundry.ADIdentifier
	containerIPs   map[string]string
	containerPorts []ContainerPort
}

// Make sure CloudFoundryService implements the Service interface
var _ Service = &CloudFoundryService{}

// NewCloudFoundryListener creates a CloudFoundryListener
func NewCloudFoundryListener(ServiceListernerDeps) (ServiceListener, error) {
	bbsCache, err := cloudfoundry.GetGlobalBBSCache()
	if err != nil {
		return nil, err
	}
	return &CloudFoundryListener{
		services:      map[string]Service{},
		stop:          make(chan bool),
		refreshTicker: time.NewTicker(10 * time.Second),
		bbsCache:      bbsCache,
	}, nil
}

// Listen periodically refreshes services from global BBS API cache
func (l *CloudFoundryListener) Listen(newSvc chan<- Service, delSvc chan<- Service) {
	// setup the I/O channels
	l.newService = newSvc
	l.delService = delSvc

	go func() {
		l.refreshServices()
		for {
			select {
			case <-l.stop:
				l.refreshTicker.Stop()
				return
			case <-l.refreshTicker.C:
				l.refreshServices()
			}
		}
	}()
}

func (l *CloudFoundryListener) refreshServices() {
	log.Debug("Refreshing services via CloudFoundryListener")
	// make sure that we can't have two simultaneous runs of this function
	l.Lock()
	defer l.Unlock()
	l.refreshCount++
	allActualLRPs, desiredLRPs := l.bbsCache.GetAllLRPs()

	// if not found and running, add it
	// at the end, compare what we saw and what is cached and kill what's not there anymore
	notSeen := make(map[string]interface{})
	for i := range l.services {
		notSeen[i] = nil
	}

	adIdentifiers := l.getAllADIdentifiers(desiredLRPs, allActualLRPs)
	for _, id := range adIdentifiers {
		strID := id.String()
		if _, found := l.services[strID]; found {
			// delete is no-op when we try to delete a key that doesn't exist
			// NOTE: this will remove old versions of services on redeploys because ADIdentifier contains ProcessGUID,
			//       which changes by redeploying
			delete(notSeen, strID)
			continue
		}
		svc := l.createService(id)
		// if the container is not in RUNNING state, we can't populate ports, thus createService would return nil
		if svc != nil {
			l.newService <- svc
		}
	}

	for adID := range notSeen {
		l.delService <- l.services[adID]
		delete(l.services, adID)
	}
}

func (l *CloudFoundryListener) createService(adID cloudfoundry.ADIdentifier) *CloudFoundryService {
	var svc *CloudFoundryService
	aLRP := adID.GetActualLRP()
	if aLRP == nil {
		// non-container service
		// NOTE: non-container services intentionally have no IPs or ports, everything is supposed to be configured
		// through the "variables" section in the AD configuration
		dLRP := adID.GetDesiredLRP()
		svc = &CloudFoundryService{
			adIdentifier:   adID,
			containerIPs:   map[string]string{},
			containerPorts: []ContainerPort{},
			tags:           dLRP.GetTagsFromDLRP(),
		}
	} else {
		if aLRP.State != cloudfoundry.ActualLrpStateRunning {
			return nil
		}
		// container service => we need one service per container instance
		ips := map[string]string{CfServiceContainerIP: aLRP.ContainerIP}
		ports := []ContainerPort{}
		for _, p := range aLRP.Ports {
			ports = append(ports, ContainerPort{
				// NOTE: because of how configresolver.getPort works, we can't use e.g. port_8080, so we use port_p8080
				Name: fmt.Sprintf("p%d", p),
				Port: int(p),
			})
		}
		nodeTags, err := l.bbsCache.GetTagsForNode(aLRP.CellID)
		if err != nil {
			log.Errorf("Error getting node tags: %v", err)
		}
		tags, ok := nodeTags[aLRP.InstanceGUID]
		if !ok {
			log.Errorf("Could not find tags for instance %s", aLRP.InstanceGUID)
		}
		svc = &CloudFoundryService{
			tags:           tags,
			adIdentifier:   adID,
			containerIPs:   ips,
			containerPorts: ports,
		}
	}
	l.services[adID.String()] = svc
	return svc
}

func (l *CloudFoundryListener) getAllADIdentifiers(desiredLRPs map[string]*cloudfoundry.DesiredLRP, actualLRPs map[string][]*cloudfoundry.ActualLRP) []cloudfoundry.ADIdentifier {
	ret := []cloudfoundry.ADIdentifier{}
	for _, dLRP := range desiredLRPs {
		for adName := range dLRP.EnvAD {
			if _, ok := dLRP.EnvVcapServices[adName]; ok {
				// if it's in VCAP_SERVICES, it's a non-container service and we want one instance per App
				ret = append(ret, cloudfoundry.NewADNonContainerIdentifier(*dLRP, adName))
			} else {
				// if it's not in VCAP_SERVICES, it's a container service and we want one instance per container
				aLRPs, ok := actualLRPs[dLRP.ProcessGUID]
				if !ok {
					aLRPs = []*cloudfoundry.ActualLRP{}
				}
				for _, aLRP := range aLRPs {
					ret = append(ret, cloudfoundry.NewADContainerIdentifier(*dLRP, adName, *aLRP))
				}
			}
		}
	}
	return ret
}

// Stop queues a shutdown of CloudFoundryListener
func (l *CloudFoundryListener) Stop() {
	l.stop <- true
}

// Equal returns whether the two CloudFoundlyService are equal
func (s *CloudFoundryService) Equal(o Service) bool {
	s2, ok := o.(*CloudFoundryService)
	if !ok {
		return false
	}

	return reflect.DeepEqual(s.adIdentifier, s2.adIdentifier) &&
		reflect.DeepEqual(s.tags, s2.tags) &&
		reflect.DeepEqual(s.containerIPs, s2.containerIPs) &&
		reflect.DeepEqual(s.containerPorts, s2.containerPorts)
}

// GetServiceID returns the unique entity name linked to that service
func (s *CloudFoundryService) GetServiceID() string {
	return s.adIdentifier.String()
}

// GetADIdentifiers returns a set of AD identifiers for a container.
func (s *CloudFoundryService) GetADIdentifiers() []string {
	return []string{s.adIdentifier.String()}
}

// GetHosts returns the container's hosts
func (s *CloudFoundryService) GetHosts() (map[string]string, error) {
	return s.containerIPs, nil
}

// GetPorts returns the container's ports
func (s *CloudFoundryService) GetPorts() ([]ContainerPort, error) {
	return s.containerPorts, nil
}

// GetTags returns the list of container tags
func (s *CloudFoundryService) GetTags() ([]string, error) {
	return s.tags, nil
}

// GetTagsWithCardinality returns the tags with given cardinality. Not supported in CF
func (s *CloudFoundryService) GetTagsWithCardinality(_ string) ([]string, error) {
	return s.GetTags()
}

// GetPid returns nil and an error because pids are currently not supported in CF
func (s *CloudFoundryService) GetPid() (int, error) {
	return -1, ErrNotSupported
}

// GetHostname returns nil and an error because hostnames are not supported in CF
func (s *CloudFoundryService) GetHostname() (string, error) {
	return "", ErrNotSupported
}

// IsReady always returns true on CF
func (s *CloudFoundryService) IsReady() bool {
	return true
}

// HasFilter returns false on CF
func (s *CloudFoundryService) HasFilter(_ filter.Scope) bool {
	return false
}

// GetExtraConfig isn't supported
func (s *CloudFoundryService) GetExtraConfig(_ string) (string, error) {
	return "", ErrNotSupported
}

// FilterTemplates does nothing.
func (s *CloudFoundryService) FilterTemplates(map[string]integration.Config) {
}
