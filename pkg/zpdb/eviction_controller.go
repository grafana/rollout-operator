package zpdb

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	"github.com/pkg/errors"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	logDenyMesg  = "pod eviction denied"
	logAllowMesg = "pod eviction allowed"
)

type EvictionController struct {
	// a lock used to control finding a specific named lock
	lock sync.RWMutex

	// a lock for each (zpdb) config name
	locks map[string]*sync.Mutex

	kubeClient kubernetes.Interface
	logger     log.Logger

	cfgObserver *configObserver
	podObserver *podObserver

	resolver spanlogger.TenantResolver
}

func NewEvictionController(kubeClient kubernetes.Interface, dynamicClient dynamic.Interface, namespace string, logger log.Logger) *EvictionController {

	// watches for ZoneAwarePodDisruptionBudget configurations being applied and maintains a zpdb configuration configCache
	cfgObserver := newConfigObserver(dynamicClient, namespace, logger)

	// watches for Pod changes which are reflected into the pod eviction configCache
	podObserver := newPodObserver(kubeClient, namespace, logger)

	return &EvictionController{
		locks:       make(map[string]*sync.Mutex, 5),
		lock:        sync.RWMutex{},
		cfgObserver: cfgObserver,
		podObserver: podObserver,
		kubeClient:  kubeClient,
		logger:      logger,
		resolver:    util.NoTenantResolver{},
	}
}

func (c *EvictionController) Start() error {
	if err := c.cfgObserver.start(); err != nil {
		return errors.Wrap(err, "failed to start zpdb config observer")
	}
	if err := c.podObserver.start(); err != nil {
		return errors.Wrap(err, "failed to start zpdb pod observer")
	}
	return nil
}

func (c *EvictionController) Stop() {
	c.cfgObserver.stop()
	c.podObserver.stop()
}

// findLock returns a Mutex for each unique name, creating the Mutex if required.
func (c *EvictionController) findLock(name string) *sync.Mutex {
	c.lock.RLock()
	if pdbLock, exists := c.locks[name]; exists {
		c.lock.RUnlock()
		return pdbLock
	}
	c.lock.RUnlock()

	c.lock.Lock()
	defer c.lock.Unlock()

	if pdbLock, exists := c.locks[name]; exists {
		return pdbLock
	}

	c.locks[name] = &sync.Mutex{}
	return c.locks[name]
}

// MarkPodAsDeleted allows for a programmatic pod eviction request. It returns an error if the pod eviction is denied.
// Note that if this func returns without an error, the pod will be marked as pending eviction within the zpdb eviction cache.
// Note also that this func does not actually evict or delete the pod from kubernetes.
func (c *EvictionController) MarkPodAsDeleted(ctx context.Context, namespace string, podName string, source string) error {
	request := v1.AdmissionReview{
		Request: &v1.AdmissionRequest{
			// not a real uid. The eviction_controller only uses this for logging purposes
			UID: types.UID(fmt.Sprintf("internal-request-pod-eviction-%s-for-rolling-update", podName)),
			Kind: metav1.GroupVersionKind{
				Group:   "policy",
				Version: "v1",
				Kind:    "Eviction",
			},
			Resource: metav1.GroupVersionResource{
				Group:    "policy",
				Version:  "v1",
				Resource: "evictions",
			},
			Name:      podName,
			Namespace: namespace,
			Operation: v1.Create,
			UserInfo: authenticationv1.UserInfo{
				Username: source,
			},
			SubResource: "eviction",
			Object: runtime.RawExtension{
				Raw: []byte(fmt.Sprintf(`{
					"apiVersion": "policy/v1",
					"kind": "Eviction",
					"metadata": {
						"name": "%s",
						"namespace": "%s"
					}
				}`, podName, namespace)),
			},
		},
	}

	response := c.HandlePodEvictionRequest(ctx, request)
	if !response.Allowed {
		return errors.New(response.Result.Message)
	}
	return nil
}

func (c *EvictionController) HandlePodEvictionRequest(ctx context.Context, ar v1.AdmissionReview) *v1.AdmissionResponse {
	logger, _ := spanlogger.New(ctx, c.logger, "admission.PodEviction()", c.resolver)
	defer logger.Finish()

	request := admissionReviewRequest{req: ar, log: logger, client: k8sClient{ctx: ctx, kubeClient: c.kubeClient}}

	// set up key=value pairs we include on all logs within this context
	request.initLogger()

	level.Debug(logger).Log("msg", "considering pod eviction")

	// validate that this is a valid pod eviction create request
	if err := request.validate(); err != nil {
		level.Warn(request.log).Log("msg", logDenyMesg, "reason", "not a valid create pod eviction request", "err", err)
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	}

	// get the pod which has been requested for eviction
	var pod *corev1.Pod
	var err error
	if pod, err = request.client.podByName(request.req.Request.Namespace, request.req.Request.Name); err != nil {
		level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to find pod by name", "err", err)
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	}

	level.Debug(request.log).Log(
		"msg", "found pod",
		"generation-observed", pod.Generation,
		"reason", pod.Status.Reason,
		"phase", pod.Status.Phase,
		"creation-timestamp", pod.CreationTimestamp,
		"deletion-timestamp", pod.DeletionTimestamp,
	)

	// evicting a terminal pod should result in direct deletion of pod as it already caused disruption by the time we are evicting.
	if request.canIgnorePDBForPod(pod) {
		level.Info(request.log).Log("msg", logAllowMesg, "reason", "pod is not ready")
		return request.allowWithWarning("pod is not ready")
	}

	// find the pdb which applies to this pod
	// for pods not managed by a zpdb we allow the request we allow the request and Kubernetes will continue to apply any other static PDB
	pdbConfig, err := c.cfgObserver.pdbCache.find(pod)
	if err != nil {
		level.Error(request.log).Log("msg", logDenyMesg, "err", err)
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	} else if pdbConfig == nil {
		level.Debug(request.log).Log("msg", logAllowMesg, "reason", "pod is not within a zpdb scope")
		return request.allow()
	}

	// get the StatefulSet who manages this pod
	var sts *appsv1.StatefulSet
	if sts, err = request.client.owner(pod); err != nil {
		level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to find pod owner", "err", err)
		return request.denyWithReason(err.Error(), http.StatusInternalServerError)
	}

	// ie ingester-zone-a
	request.log.SetSpanAndLogTag("owner", sts.Name)

	lock := c.findLock(pdbConfig.name)
	lock.Lock()
	defer lock.Unlock()

	// the number of allowed unavailable pods in other zones.
	maxUnavailable := pdbConfig.maxUnavailablePods(sts)
	if maxUnavailable == 0 {
		level.Info(request.log).Log("msg", logDenyMesg, "reason", "max unavailable = 0")
		return request.denyWithReason("max unavailable = 0", http.StatusForbidden)
	}

	// Find the all StatefulSets which span all zones.
	// Assumption - each StatefulSet manages all the pods for a single zone. This list of StatefulSets covers all zones.
	// During a migration it may be possible to break this assumption, but during a migration the maxUnavailable will be set to 0 and the eviction request will be denied.
	allStatefulSets, err := request.client.findRelatedStatefulSets(sts.Namespace, pdbConfig.selector)
	if err != nil || allStatefulSets == nil || len(allStatefulSets.Items) <= 1 {
		if err != nil {
			level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to find related stateful sets - a minimum of 2 StatefulSets is required", "err", err)
		} else {
			level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to find related stateful sets - a minimum of 2 StatefulSets is required")
		}

		return request.denyWithReason("minimum number of StatefulSets not found", http.StatusBadRequest)
	}

	// this is the partition of the pod being evicted - for classic zones the partition will be an empty string
	partition, err := pdbConfig.podPartition(pod)
	if err != nil {
		level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to determine partition for pod", "err", err)
		return request.denyWithReason(err.Error(), http.StatusBadRequest)
	}

	// include the partition we are considering
	if len(partition) > 0 {
		request.log.SetSpanAndLogTag("partition", partition)
	}

	// include the number of zones we will be considering
	request.log.SetSpanAndLogTag("zones", len(allStatefulSets.Items))

	var pdb validator

	if len(partition) > 0 {
		// partition mode - the pod tallies are applied to all pods in other zones which relate to this partition
		pdb = newValidatorPartitionAware(sts, partition, len(allStatefulSets.Items), pdbConfig, request.log)
	} else {
		// zone mode - we allow the eviction if no other zone is disrupted and the max unavailable within the eviction zone is not exceeded
		pdb = newValidatorZoneAware(sts, len(allStatefulSets.Items))
	}

	for _, otherSts := range allStatefulSets.Items {
		// test on whether we exclude the eviction pod's StatefulSet from the assessment
		if !pdb.considerSts(&otherSts) {
			continue
		}

		result, err := request.client.podsNotRunningAndReady(&otherSts, pod, pdb.considerPod(), c.podObserver.podEvictCache)
		if err != nil {
			level.Error(request.log).Log("msg", logDenyMesg, "reason", "unable to determine pod status in related StatefulSet", "sts", otherSts.Name)
			return request.denyWithReason("unable to determine pod status in related StatefulSet", http.StatusInternalServerError)
		}

		if err = pdb.accumulateResult(&otherSts, result); err != nil {
			// opportunity to fail fast
			level.Info(request.log).Log("msg", logDenyMesg, "err", err, "sts", otherSts.Name)
			return request.denyWithReason(err.Error(), http.StatusTooManyRequests)
		}

	}

	if err = pdb.validate(maxUnavailable); err != nil {
		level.Info(request.log).Log("msg", logDenyMesg, "reason", "zpdb exceeded", "err", err)
		return request.denyWithReason(err.Error(), http.StatusTooManyRequests)
	}

	// mark the pod as evicted
	// this entry will self expire and will be removed when a pod state change is observed
	c.podObserver.podEvictCache.recordEviction(pod)

	level.Info(request.log).Log("msg", logAllowMesg, "reason", pdb.successMessage())
	return request.allow()
}
