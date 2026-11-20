package zpdb

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	v1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// defaultLiveCheckInterval bounds how long a Pod can be denied from the cache before a retry goes live.
	// It only matters if the cache stops following the API, such as during a stalled watch: recovery the
	// cache observes is seen on the next retry.
	defaultLiveCheckInterval = 30 * time.Second

	// defaultLiveDeadlineCapacity bounds the Pods tracked at once. A dropped entry sends that Pod's next
	// request to the live path, so the capacity only affects cost.
	defaultLiveDeadlineCapacity = 4096
)

// liveDeadlines records until when each Pod's retries may be denied from the cache. Only a completed live
// budget denial sets a deadline, and cached denials never extend it, so a stalled watch can delay each Pod
// by at most one interval, plus its retry cadence, regardless of other Pods' retries.
type liveDeadlines struct {
	mu       sync.Mutex
	capacity int
	entries  map[types.UID]time.Time
}

func newLiveDeadlines(capacity int) *liveDeadlines {
	return &liveDeadlines{capacity: capacity, entries: make(map[types.UID]time.Time)}
}

// valid reports whether a cached denial may be served for the Pod at the given time.
func (d *liveDeadlines) valid(uid types.UID, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	deadline, ok := d.entries[uid]
	return ok && now.Before(deadline)
}

// set records the Pod's deadline. At capacity it drops expired entries first, then any entry.
func (d *liveDeadlines) set(uid types.UID, deadline, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.entries[uid]; !ok && len(d.entries) >= d.capacity {
		for k, v := range d.entries {
			if !now.Before(v) {
				delete(d.entries, k)
			}
		}
		for k := range d.entries {
			if len(d.entries) < d.capacity {
				break
			}
			delete(d.entries, k)
		}
	}
	d.entries[uid] = deadline
}

func (d *liveDeadlines) delete(uid types.UID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, uid)
}

// startCachedDenials lets the Pod's retries be denied from the cache until the live check interval after
// start. A zero interval sets no deadline, so every retry goes live.
func (c *EvictionController) startCachedDenials(pod *corev1.Pod, start time.Time) {
	if c.liveCheckInterval <= 0 {
		return
	}
	c.liveDeadlines.set(pod.UID, start.Add(c.liveCheckInterval), c.now())
}

// newValidator returns the budget check for the eviction pod's partition, or for its zone when the budget
// isn't partition-aware.
func (c *EvictionController) newValidator(sts *appsv1.StatefulSet, partition string, zones int, pdbConfig *config, log *spanlogger.SpanLogger) validator {
	if len(partition) > 0 {
		// partition mode - the pod tallies are applied to all pods in other zones which relate to this partition
		return newValidatorPartitionAware(sts, partition, zones, pdbConfig, c.podObserver.podEvictCache, log, c.now)
	}
	// zone mode - we allow the eviction if no other zone is disrupted and the max unavailable within the eviction zone is not exceeded
	return newValidatorZoneAware(sts, zones, c.podObserver.podEvictCache)
}

// denyFromCache evaluates the budget against the informer cache and returns a denial if it is exceeded.
// Stale cache state can only delay an eviction, so the cache is never used to allow one: anything other
// than a budget denial returns false and the caller continues with live reads. The cached denial is only
// served while the Pod has a live deadline, which a completed live budget denial sets.
func (c *EvictionController) denyFromCache(request *admissionReviewRequest, pod *corev1.Pod, pdbConfig *config, now time.Time) (*v1.AdmissionResponse, bool) {
	if !c.podObserver.cacheSynced() || !c.liveDeadlines.valid(pod.UID, now) || request.canIgnorePDBForPod(pod) {
		return nil, false
	}
	ownerRef, err := statefulSetOwnerReference(pod)
	if err != nil {
		return nil, false
	}
	related, err := c.podObserver.stsLister.StatefulSets(pod.Namespace).List(*pdbConfig.selector)
	if err != nil || len(related) <= 1 {
		return nil, false
	}
	// The live path sees StatefulSets in name order, which decides the zone named in a denial.
	slices.SortFunc(related, func(a, b *appsv1.StatefulSet) int { return strings.Compare(a.Name, b.Name) })
	var sts *appsv1.StatefulSet
	for _, s := range related {
		if s.Name == ownerRef.Name && s.UID == ownerRef.UID {
			sts = s
			break
		}
	}
	if sts == nil {
		return nil, false
	}
	maxUnavailable := pdbConfig.maxUnavailablePods(sts)
	if maxUnavailable == 0 {
		return nil, false
	}
	partition, err := pdbConfig.podPartition(pod)
	if err != nil {
		return nil, false
	}

	pdb := c.newValidator(sts, partition, len(related), pdbConfig, request.log)
	for _, otherSts := range related {
		if !pdb.considerSts(otherSts) {
			continue
		}
		pods, err := c.podObserver.cachedZonePods(otherSts)
		if err != nil {
			return nil, false
		}
		if err := pdb.accumulateResult(otherSts, zoneStatus(otherSts, pod, pdb, pods)); err != nil {
			return c.denyCached(request, err), true
		}
	}
	if err := pdb.validate(maxUnavailable); err != nil {
		return c.denyCached(request, err), true
	}
	return nil, false
}

func (c *EvictionController) denyCached(request *admissionReviewRequest, err error) *v1.AdmissionResponse {
	level.Info(request.log).Log("msg", logDenyMesg, "reason", "zpdb exceeded in cache", "err", err)
	c.metrics.CachedDenials.Inc()
	c.metrics.EvictionRequests.WithLabelValues("denied", fmt.Sprintf("%d", http.StatusTooManyRequests)).Inc()
	return request.denyWithReason(err.Error(), http.StatusTooManyRequests)
}
