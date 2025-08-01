// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build clusterchecks && kubeapiserver

package listeners

import (
	"fmt"
	"reflect"
	"sort"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8stypes "k8s.io/apimachinery/pkg/types"
	infov1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/common/types"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/common/utils"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/providers/names"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/telemetry"
	workloadfilter "github.com/DataDog/datadog-agent/comp/core/workloadfilter/def"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	kubeServiceID    = "service"
	kubeServicesName = "kube_services"
)

// KubeServiceListener listens to kubernetes service creation
type KubeServiceListener struct {
	informer          infov1.ServiceInformer
	services          map[k8stypes.UID]Service
	promInclAnnot     types.PrometheusAnnotations
	newService        chan<- Service
	delService        chan<- Service
	targetAllServices bool
	m                 sync.RWMutex
	filterStore       workloadfilter.Component
	telemetryStore    *telemetry.Store
}

// KubeServiceService represents a Kubernetes Service
type KubeServiceService struct {
	entity          string
	tags            []string
	hosts           map[string]string
	ports           []ContainerPort
	metricsExcluded bool
	globalExcluded  bool
}

// Make sure KubeServiceService implements the Service interface
var _ Service = &KubeServiceService{}

// isServiceAnnotated returns true if the Service has an annotation with a given key
func isServiceAnnotated(ksvc *v1.Service, annotationKey string) bool {
	if ksvc == nil {
		return false
	}

	annotations := ksvc.GetAnnotations()

	if _, found := annotations[utils.KubeAnnotationPrefix+annotationKey+".checks"]; found {
		return true
	}

	if _, found := annotations[utils.KubeAnnotationPrefix+annotationKey+".instances"]; found {
		return true
	}

	return false
}

// NewKubeServiceListener returns the kube service implementation of the ServiceListener interface
func NewKubeServiceListener(options ServiceListernerDeps) (ServiceListener, error) {
	// Using GetAPIClient (no wait) as Client should already be initialized by Cluster Agent main entrypoint before
	ac, err := apiserver.GetAPIClient()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to apiserver: %s", err)
	}

	servicesInformer := ac.InformerFactory.Core().V1().Services()
	if servicesInformer == nil {
		return nil, fmt.Errorf("cannot get service informer: %s", err)
	}

	return &KubeServiceListener{
		services:          make(map[k8stypes.UID]Service),
		informer:          servicesInformer,
		promInclAnnot:     getPrometheusIncludeAnnotations(),
		targetAllServices: options.Config.IsProviderEnabled(names.KubeServicesFileRegisterName),
		telemetryStore:    options.Telemetry,
		filterStore:       options.Filter,
	}, nil
}

// Listen starts watching service events
func (l *KubeServiceListener) Listen(newSvc chan<- Service, delSvc chan<- Service) {
	// setup the I/O channels
	l.newService = newSvc
	l.delService = delSvc

	if _, err := l.informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    l.added,
		UpdateFunc: l.updated,
		DeleteFunc: l.deleted,
	}); err != nil {
		log.Errorf("Cannot add event handler to kube service informer: %s", err)
	}

	// Initial fill
	services, err := l.informer.Lister().List(labels.Everything())
	if err != nil {
		log.Errorf("Cannot list Kubernetes services: %s", err)
	}
	for _, s := range services {
		l.createService(s)
	}
}

// Stop is a stub
func (l *KubeServiceListener) Stop() {
	// We cannot deregister from the informer
}

func (l *KubeServiceListener) added(obj interface{}) {
	castedObj, ok := obj.(*v1.Service)
	if !ok {
		log.Errorf("Expected a *v1.Service type, got: %T", obj)
		return
	}
	l.createService(castedObj)
}

func (l *KubeServiceListener) deleted(obj interface{}) {
	castedObj, ok := obj.(*v1.Service)
	if !ok {
		// It's possible that we got a DeletedFinalStateUnknown here
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			log.Errorf("Received unexpected object: %T", obj)
			return
		}

		castedObj, ok = deletedState.Obj.(*v1.Service)
		if !ok {
			log.Errorf("Expected DeletedFinalStateUnknown to contain *v1.Service, got: %T", deletedState.Obj)
			return
		}
	}

	l.removeService(castedObj)
}

func (l *KubeServiceListener) updated(old, obj interface{}) {
	// Cast the updated object or return on failure
	castedObj, ok := obj.(*v1.Service)
	if !ok {
		log.Errorf("Expected a *v1.Service type, got: %T", obj)
		return
	}
	// Cast the old object, consider it an add on cast failure
	castedOld, ok := old.(*v1.Service)
	if !ok {
		log.Errorf("Expected a *v1.Service type, got: %T", old)
		l.createService(castedObj)
		return
	}
	if servicesDiffer(castedObj, castedOld) || l.promInclAnnot.AnnotationsDiffer(castedObj.GetAnnotations(), castedOld.GetAnnotations()) {
		l.removeService(castedObj)
		l.createService(castedObj)
	}
}

// servicesDiffer compares two services to only go forward
// when relevant fields are changed. This logic must be
// updated if more fields are used.
func servicesDiffer(first, second *v1.Service) bool {
	// Quick exit if resversion did not change
	if first.ResourceVersion == second.ResourceVersion {
		return false
	}
	// AD annotations - check templates
	if isServiceAnnotated(first, kubeServiceID) != isServiceAnnotated(second, kubeServiceID) {
		return true
	}
	// AD labels - standard tags
	if standardTagsDigest(first.GetLabels()) != standardTagsDigest(second.GetLabels()) {
		return true
	}
	// Cluster IP
	if first.Spec.ClusterIP != second.Spec.ClusterIP {
		return true
	}
	// Ports
	if len(first.Spec.Ports) != len(second.Spec.Ports) {
		return true
	}
	for i := range first.Spec.Ports {
		if first.Spec.Ports[i].Name != second.Spec.Ports[i].Name {
			return true
		}
		if first.Spec.Ports[i].Port != second.Spec.Ports[i].Port {
			return true
		}
	}
	// No relevant change
	return false
}

func (l *KubeServiceListener) shouldIgnore(ksvc *v1.Service) bool {
	if l.targetAllServices {
		return false
	}

	// Ignore services with no AD or Prometheus AD include annotation
	return !isServiceAnnotated(ksvc, kubeServiceID) && !l.promInclAnnot.IsMatchingAnnotations(ksvc.GetAnnotations())
}

func (l *KubeServiceListener) createService(ksvc *v1.Service) {
	if ksvc == nil {
		return
	}

	if l.shouldIgnore(ksvc) {
		return
	}

	svc := processService(ksvc, l.filterStore)

	l.m.Lock()
	l.services[ksvc.UID] = svc
	l.m.Unlock()

	l.newService <- svc
	if l.telemetryStore != nil {
		l.telemetryStore.WatchedResources.Inc(kubeServicesName, telemetry.ResourceKubeService)
	}
}

func processService(ksvc *v1.Service, filterStore workloadfilter.Component) *KubeServiceService {
	svc := &KubeServiceService{
		entity: apiserver.EntityForService(ksvc),
	}

	svc.metricsExcluded = filterStore.IsServiceExcluded(
		workloadfilter.CreateService(ksvc.Name, ksvc.Namespace, ksvc.GetAnnotations()),
		[][]workloadfilter.ServiceFilter{{workloadfilter.ServiceADAnnotationsMetrics}, {workloadfilter.LegacyServiceMetrics}},
	)

	svc.globalExcluded = filterStore.IsServiceExcluded(
		workloadfilter.CreateService(ksvc.Name, ksvc.Namespace, ksvc.GetAnnotations()),
		[][]workloadfilter.ServiceFilter{{workloadfilter.ServiceADAnnotations}, {workloadfilter.LegacyServiceGlobal}},
	)

	// Service tags
	svc.tags = []string{
		fmt.Sprintf("kube_service:%s", ksvc.Name),
		fmt.Sprintf("kube_namespace:%s", ksvc.Namespace),
	}

	// Standard tags from the service's labels
	svc.tags = append(svc.tags, getStandardTags(ksvc.GetLabels())...)

	// Hosts, only use internal ClusterIP for now
	svc.hosts = map[string]string{"cluster": ksvc.Spec.ClusterIP}

	// Ports
	var ports []ContainerPort
	for _, port := range ksvc.Spec.Ports {
		ports = append(ports, ContainerPort{int(port.Port), port.Name})
	}
	sort.Slice(ports, func(i, j int) bool {
		return ports[i].Port < ports[j].Port
	})
	svc.ports = ports
	if len(svc.ports) == 0 {
		// Port might not be specified in pod spec
		log.Debugf("No ports found for service %s", ksvc.Name)
	}

	return svc
}

func (l *KubeServiceListener) removeService(ksvc *v1.Service) {
	if ksvc == nil {
		return
	}
	l.m.RLock()
	svc, ok := l.services[ksvc.UID]
	l.m.RUnlock()

	if ok {
		l.m.Lock()
		delete(l.services, ksvc.UID)
		l.m.Unlock()

		l.delService <- svc
		if l.telemetryStore != nil {
			l.telemetryStore.WatchedResources.Dec(kubeServicesName, telemetry.ResourceKubeService)
		}
	} else {
		log.Debugf("Entity %s not found, not removing", ksvc.UID)
	}
}

// Equal returns whether the two KubeServiceService are equal
func (s *KubeServiceService) Equal(o Service) bool {
	s2, ok := o.(*KubeServiceService)
	if !ok {
		return false
	}

	return s.entity == s2.entity &&
		reflect.DeepEqual(s.tags, s2.tags) &&
		reflect.DeepEqual(s.hosts, s2.hosts) &&
		reflect.DeepEqual(s.ports, s2.ports)
}

// GetServiceID returns the unique entity name linked to that service
func (s *KubeServiceService) GetServiceID() string {
	return s.entity
}

// GetADIdentifiers returns the service AD identifiers
func (s *KubeServiceService) GetADIdentifiers() []string {
	// Only the entity for now, to match on annotation
	return []string{s.entity}
}

// GetHosts returns the pod hosts
func (s *KubeServiceService) GetHosts() (map[string]string, error) {
	return s.hosts, nil
}

// GetPid is not supported for PodContainerService
func (s *KubeServiceService) GetPid() (int, error) {
	return -1, ErrNotSupported
}

// GetPorts returns the container's ports
func (s *KubeServiceService) GetPorts() ([]ContainerPort, error) {
	return s.ports, nil
}

// GetTags retrieves tags
func (s *KubeServiceService) GetTags() ([]string, error) {
	return s.tags, nil
}

// GetTagsWithCardinality returns the tags with given cardinality.
func (s *KubeServiceService) GetTagsWithCardinality(_ string) ([]string, error) {
	return s.GetTags()
}

// GetHostname returns nil and an error because port is not supported in Kubelet
func (s *KubeServiceService) GetHostname() (string, error) {
	return "", ErrNotSupported
}

// IsReady returns if the service is ready
func (s *KubeServiceService) IsReady() bool {
	return true
}

// HasFilter returns whether the kube service should not collect certain metrics
// due to filtering applied.
func (s *KubeServiceService) HasFilter(fs workloadfilter.Scope) bool {
	switch fs {
	case workloadfilter.MetricsFilter:
		return s.metricsExcluded
	case workloadfilter.GlobalFilter:
		return s.globalExcluded
	default:
		return false
	}
}

// GetExtraConfig isn't supported
func (s *KubeServiceService) GetExtraConfig(_ string) (string, error) {
	return "", ErrNotSupported
}

// FilterTemplates does nothing.
func (s *KubeServiceService) FilterTemplates(map[string]integration.Config) {
}
