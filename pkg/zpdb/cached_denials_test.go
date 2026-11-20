package zpdb

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testPartitionRegex = "ingester-zone-[a-z]-([0-9]+)"
	// retryInterval is how often the tests' simulated clients retry a denied eviction.
	retryInterval = 10 * time.Second
)

func newPartitionBudget() *unstructured.Unstructured {
	return newPDBMaxUnavailableWithRegex(1, rolloutGroupValue, testPartitionRegex, 1)
}

// cachedDenialFixture runs a controller whose cache changes only when the test delivers a watch event.
// A change made through the client alone reaches the live API but not the cache, as with a stalled watch,
// and delivered events make cache updates deterministic.
type cachedDenialFixture struct {
	t          testing.TB
	client     *fake.Clientset
	controller *EvictionController
	podWatch   *watch.FakeWatcher
	stsWatch   *watch.FakeWatcher
	pods       map[string]*corev1.Pod
	sets       map[string]*appsv1.StatefulSet
	now        time.Time
}

// newCachedDenialFixture creates zones a and b with the given replicas each, all Running and Ready.
func newCachedDenialFixture(t testing.TB, budget *unstructured.Unstructured, replicas int32) *cachedDenialFixture {
	// Start from the real time so that any check reading the wall clock instead of the controller's clock
	// disagrees with the live path once the test advances the clock.
	f := &cachedDenialFixture{t: t, pods: map[string]*corev1.Pod{}, sets: map[string]*appsv1.StatefulSet{}, now: time.Now()}
	var objects []runtime.Object
	for _, zone := range []string{statefulSetZoneA, statefulSetZoneB} {
		sts := newEvictionControllerSts(zone)
		*sts.Spec.Replicas = replicas
		sts.Status.Replicas = replicas
		f.sets[zone] = sts
		objects = append(objects, sts)
		for i := range replicas {
			pod := newPod(fmt.Sprintf("%s-%d", zone, i), sts)
			f.pods[pod.Name] = pod
			objects = append(objects, pod)
		}
	}
	f.client = fake.NewClientset(objects...)
	f.podWatch = watch.NewFakeWithChanSize(100, false)
	f.stsWatch = watch.NewFakeWithChanSize(100, false)
	f.client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) { return true, f.podWatch, nil })
	f.client.PrependWatchReactor("statefulsets", func(k8stesting.Action) (bool, watch.Interface, error) { return true, f.stsWatch, nil })
	f.controller = NewEvictionController(f.client, newFakeDynamicClient(), testNamespace, newTestPodsFactory(f.client), log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
	f.controller.now = func() time.Time { return f.now }
	require.NoError(t, f.controller.Start())
	t.Cleanup(f.controller.Stop)
	waitForCacheSync(t, f.controller)
	_, _, err := f.controller.cfgObserver.pdbCache.addOrUpdateRaw(budget)
	require.NoError(t, err)
	return f
}

func (f *cachedDenialFixture) admit(name string) *admissionv1.AdmissionResponse {
	return f.controller.HandlePodEvictionRequest(f.t.Context(), createBasicEvictionAdmissionReview(name, testNamespace), NewMaxUnavailableZeroOverrideNone())
}

// updatePod changes the Pod in the API and, if toCache is set, delivers the change to the cache.
func (f *cachedDenialFixture) updatePod(name string, toCache bool, change func(*corev1.Pod)) {
	pod := f.pods[name].DeepCopy()
	change(pod)
	_, err := f.client.CoreV1().Pods(testNamespace).Update(f.t.Context(), pod, metav1.UpdateOptions{})
	require.NoError(f.t, err)
	f.pods[name] = pod
	if toCache {
		f.podWatch.Modify(pod)
		require.Eventually(f.t, func() bool {
			cached, err := f.controller.podObserver.podLister.Pods(testNamespace).Get(name)
			return err == nil && equality.Semantic.DeepEqual(cached, pod)
		}, 10*time.Second, time.Millisecond)
	}
}

// updateStatefulSet changes the StatefulSet in the API and delivers the change to the cache.
func (f *cachedDenialFixture) updateStatefulSet(name string, change func(*appsv1.StatefulSet)) {
	sts := f.sets[name].DeepCopy()
	change(sts)
	_, err := f.client.AppsV1().StatefulSets(testNamespace).Update(f.t.Context(), sts, metav1.UpdateOptions{})
	require.NoError(f.t, err)
	f.sets[name] = sts
	f.stsWatch.Modify(sts)
	require.Eventually(f.t, func() bool {
		cached, err := f.controller.podObserver.stsLister.StatefulSets(testNamespace).Get(name)
		return err == nil && equality.Semantic.DeepEqual(cached, sts)
	}, 10*time.Second, time.Millisecond)
}

func unready(pod *corev1.Pod) { pod.Status.Phase = corev1.PodPending }
func ready(pod *corev1.Pod)   { pod.Status.Phase = corev1.PodRunning }

// A retry within the deadline is denied from the cache without any API request, so a retry storm neither
// spends the client's rate limit nor decodes live Pod lists. Both budget kinds reach the cached check
// through different validators.
func TestEvictionController_HandlePodEvictionRequest_CachedDenialSkipsAPI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget *unstructured.Unstructured
	}{
		{name: "zone-aware", budget: newPDBMaxUnavailable(1, rolloutGroupValue)},
		{name: "partition-aware", budget: newPartitionBudget()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCachedDenialFixture(t, tc.budget, 1)
			f.updatePod(testPodZoneB0, true, unready)
			f.client.ClearActions()
			live := f.admit(testPodZoneA0)
			require.EqualValues(t, http.StatusTooManyRequests, live.Result.Code)
			require.NotEmpty(t, f.client.Actions(), "the first denial must use live reads")

			f.client.ClearActions()
			f.now = f.now.Add(retryInterval)
			cached := f.admit(testPodZoneA0)
			require.Equal(t, live.Result, cached.Result)
			require.Empty(t, f.client.Actions())
			require.Equal(t, 1., testutil.ToFloat64(f.controller.metrics.CachedDenials))
			require.Equal(t, 2., testutil.ToFloat64(f.controller.metrics.EvictionRequests.WithLabelValues("denied", "429")))
		})
	}
}

// Cached denials never extend a Pod's deadline, so the first retry at the deadline goes live. This bounds
// how long a stalled watch can hold a Pod back.
func TestEvictionController_HandlePodEvictionRequest_DeadlineNotExtended(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.updatePod(testPodZoneB0, true, unready)
	start := f.now
	require.False(t, f.admit(testPodZoneA0).Allowed)

	f.client.ClearActions()
	f.now = start.Add(f.controller.liveCheckInterval - time.Millisecond)
	require.False(t, f.admit(testPodZoneA0).Allowed)
	require.Empty(t, f.client.Actions(), "a retry before the deadline is denied from the cache")

	f.now = start.Add(f.controller.liveCheckInterval)
	require.False(t, f.admit(testPodZoneA0).Allowed)
	require.NotEmpty(t, f.client.Actions(), "the retry at the deadline must go live")
}

// The deadline runs from the start of the live request, so time spent queued behind the budget lock counts
// and a slow live check can't extend how long its denial is served from the cache.
func TestEvictionController_HandlePodEvictionRequest_DeadlineStartsBeforeLock(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.updatePod(testPodZoneB0, true, unready)
	lock := f.controller.findLock(rolloutGroupValue + "-rollout")
	lock.Lock()
	gotTarget := make(chan struct{}, 1)
	f.client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case gotTarget <- struct{}{}:
		default:
		}
		return false, nil, nil
	})
	start := f.now
	denied := make(chan bool)
	go func() { denied <- !f.admit(testPodZoneA0).Allowed }()
	// The request has read the clock and fetched the target, and now waits for the lock.
	<-gotTarget
	f.now = start.Add(f.controller.liveCheckInterval)
	lock.Unlock()
	require.True(t, <-denied)

	f.client.ClearActions()
	require.False(t, f.admit(testPodZoneA0).Allowed)
	require.NotEmpty(t, f.client.Actions(), "the deadline passed while the first request waited for the lock")
}

// A stalled watch can't starve one Pod behind another's retries. Each Pod has its own deadline, so B's
// recovery is seen at B's deadline even though A, which always retries just before B, stays blocked.
func TestEvictionController_HandlePodEvictionRequest_StalledWatch(t *testing.T) {
	const podA, podB, peerA, peerB = testPodZoneA0, testPodZoneA1, testPodZoneB0, testPodZoneB1
	for _, tc := range []struct {
		name     string
		capacity int
		// allowedAfter is when B's retry first goes live and sees its peer's recovery.
		allowedAfter time.Duration
	}{
		{name: "deadline", capacity: defaultLiveDeadlineCapacity, allowedAfter: defaultLiveCheckInterval},
		// With room for one entry, recording A's deadline drops B's, so B's next retry goes live.
		{name: "evicted entry", capacity: 1, allowedAfter: retryInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCachedDenialFixture(t, newPartitionBudget(), 2)
			f.controller.liveDeadlines = newLiveDeadlines(tc.capacity)
			f.updatePod(peerA, true, unready)
			f.updatePod(peerB, true, unready)
			start := f.now
			require.False(t, f.admit(podB).Allowed)
			require.False(t, f.admit(podA).Allowed)

			// The watch stalls, so only the API sees B's peer recover.
			f.updatePod(peerB, false, ready)
			for elapsed := retryInterval; elapsed <= tc.allowedAfter; elapsed += retryInterval {
				f.now = start.Add(elapsed)
				require.False(t, f.admit(podA).Allowed, "A's peer is still unready")
				require.Equal(t, elapsed == tc.allowedAfter, f.admit(podB).Allowed, "B after %s", elapsed)
			}
		})
	}
}

// The cache is never used to allow an eviction. Here the peer has just become unready and the cache hasn't
// seen it, so the cached check finds nothing to deny and the live path denies the retry.
func TestEvictionController_HandlePodEvictionRequest_CacheBehindDisruption(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.updatePod(testPodZoneB0, false, unready)
	require.False(t, f.admit(testPodZoneA0).Allowed)

	f.client.ClearActions()
	f.now = f.now.Add(retryInterval)
	require.False(t, f.admit(testPodZoneA0).Allowed)
	require.NotEmpty(t, f.client.Actions(), "the denial must come from live reads")
}

// A terminating target is allowed like any Pod the budget no longer applies to, even while it has a
// deadline and its peer is still unready.
func TestEvictionController_HandlePodEvictionRequest_TerminatingTarget(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.updatePod(testPodZoneB0, true, unready)
	require.False(t, f.admit(testPodZoneA0).Allowed)

	deleted := metav1.NewTime(f.now)
	f.updatePod(testPodZoneA0, true, func(pod *corev1.Pod) { pod.DeletionTimestamp = &deleted })
	f.now = f.now.Add(retryInterval)
	response := f.admit(testPodZoneA0)
	require.True(t, response.Allowed)
	require.Equal(t, []string{"pod is not ready"}, response.Warnings)
}

// The cross-zone delay running out changes no object, so no event signals it. The cached check evaluates
// the delay when the request arrives, so the first retry after it runs out goes live and is allowed
// without waiting for the deadline.
func TestEvictionController_HandlePodEvictionRequest_CrossZoneDelayExpires(t *testing.T) {
	const delay = 20 * time.Minute
	f := newCachedDenialFixture(t, rawConfigWithCrossZoneEvictionDelay(rolloutGroupValue+"-rollout", rolloutGroupValue, 1, 1, testPartitionRegex, delay.String()), 1)
	// The peer became Ready 5s short of the delay.
	readyAt := metav1.NewTime(f.now.Add(5*time.Second - delay))
	f.updatePod(testPodZoneB0, true, func(pod *corev1.Pod) {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: readyAt}}
	})
	require.False(t, f.admit(testPodZoneA0).Allowed)

	f.now = f.now.Add(retryInterval)
	require.Less(t, retryInterval, f.controller.liveCheckInterval)
	require.True(t, f.admit(testPodZoneA0).Allowed)
}

// A StatefulSet scaling down changes the missing-replica count without a Pod event. The StatefulSet cache
// carries the change, so the next retry goes live and is allowed without waiting for the deadline.
func TestEvictionController_HandlePodEvictionRequest_MissingReplicaResolved(t *testing.T) {
	f := newCachedDenialFixture(t, newPDBMaxUnavailable(1, rolloutGroupValue), 1)
	// Zone b expects a second Pod that doesn't exist.
	f.updateStatefulSet(statefulSetZoneB, func(sts *appsv1.StatefulSet) { *sts.Spec.Replicas = 2; sts.Status.Replicas = 2 })
	require.False(t, f.admit(testPodZoneA0).Allowed)

	f.updateStatefulSet(statefulSetZoneB, func(sts *appsv1.StatefulSet) { *sts.Spec.Replicas = 1; sts.Status.Replicas = 1 })
	f.now = f.now.Add(retryInterval)
	require.True(t, f.admit(testPodZoneA0).Allowed)
}

// Cached denials read objects shared with the informer while it applies updates. Run with -race, this
// guards against the cached check racing with the informer or modifying cached objects.
func TestEvictionController_HandlePodEvictionRequest_ConcurrentCachedDenials(t *testing.T) {
	const zonePods, retries = 4, 50
	f := newCachedDenialFixture(t, newPartitionBudget(), zonePods)
	for i := range zonePods {
		f.updatePod(fmt.Sprintf("%s-%d", statefulSetZoneB, i), true, unready)
		require.False(t, f.admit(fmt.Sprintf("%s-%d", statefulSetZoneA, i)).Allowed)
	}

	var wg sync.WaitGroup
	for i := range zonePods {
		wg.Go(func() {
			for range retries {
				assert.False(t, f.admit(fmt.Sprintf("%s-%d", statefulSetZoneA, i)).Allowed)
			}
		})
	}
	// Meanwhile the informer replaces the peers, which stay unready.
	for i := range retries {
		peer := f.pods[fmt.Sprintf("%s-%d", statefulSetZoneB, i%zonePods)].DeepCopy()
		peer.Annotations = map[string]string{"update": fmt.Sprint(i)}
		f.podWatch.Modify(peer)
	}
	wg.Wait()
	require.Equal(t, float64(zonePods*retries), testutil.ToFloat64(f.controller.metrics.CachedDenials))
}

// A dry run asks whether an eviction can proceed now, so it ignores a cached denial and uses live reads.
// How dry runs are detected is covered by TestEvictionController_HandlePodEvictionRequest_DryRun.
func TestEvictionController_HandlePodEvictionRequest_DryRunIgnoresCachedDenial(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.updatePod(testPodZoneB0, true, unready)
	require.False(t, f.admit(testPodZoneA0).Allowed)
	// The peer recovers, but the cache doesn't see it.
	f.updatePod(testPodZoneB0, false, ready)

	// kubectl drain --dry-run=server sets dryRun in the eviction body.
	probe := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	probe.Request.Object.Raw = fmt.Appendf(nil, `{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":%q,"namespace":%q},"deleteOptions":{"dryRun":["All"]}}`, testPodZoneA0, testNamespace)
	require.True(t, f.controller.HandlePodEvictionRequest(t.Context(), probe, NewMaxUnavailableZeroOverrideNone()).Allowed)
}

// A zero interval turns cached denials off, so every retry uses live reads.
func TestEvictionController_HandlePodEvictionRequest_CachedDenialsDisabled(t *testing.T) {
	f := newCachedDenialFixture(t, newPartitionBudget(), 1)
	f.controller.SetCachedDenialLiveCheckInterval(0)
	f.updatePod(testPodZoneB0, true, unready)
	require.False(t, f.admit(testPodZoneA0).Allowed)

	f.client.ClearActions()
	f.now = f.now.Add(retryInterval)
	require.False(t, f.admit(testPodZoneA0).Allowed)
	require.NotEmpty(t, f.client.Actions(), "the retry must use live reads")
	require.Zero(t, testutil.ToFloat64(f.controller.metrics.CachedDenials))
}

// BenchmarkEvictionController_HandlePodEvictionRequest_DeniedRetry compares a denied retry that uses live
// reads with one denied from the cache, for zones of 256 Pods. A cached denial must make no API requests.
func BenchmarkEvictionController_HandlePodEvictionRequest_DeniedRetry(b *testing.B) {
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("cached=%t", cached), func(b *testing.B) {
			f := newCachedDenialFixture(b, newPartitionBudget(), 256)
			f.updatePod(testPodZoneB0, true, unready)
			require.False(b, f.admit(testPodZoneA0).Allowed)
			f.client.ClearActions()
			b.ReportAllocs()
			for b.Loop() {
				if !cached {
					// Without a deadline, the retry goes live.
					b.StopTimer()
					f.controller.liveDeadlines = newLiveDeadlines(defaultLiveDeadlineCapacity)
					b.StartTimer()
				}
				if f.admit(testPodZoneA0).Allowed {
					b.Fatal("budget unexpectedly allowed the eviction")
				}
			}
			b.ReportMetric(float64(len(f.client.Actions()))/float64(b.N), "api-requests/op")
		})
	}
}

// The rollout controller's requests always use live reads. They neither use a cached denial nor set a
// deadline that would let webhook retries be denied from the cache.
func TestEvictionController_MarkPodAsDeleted_UsesLiveReads(t *testing.T) {
	markDeleted := func(f *cachedDenialFixture) error {
		return f.controller.MarkPodAsDeleted(t.Context(), testNamespace, testPodZoneA0, "rollout-controller", NewMaxUnavailableZeroOverrideNone())
	}
	t.Run("ignores cached denial", func(t *testing.T) {
		f := newCachedDenialFixture(t, newPartitionBudget(), 1)
		f.updatePod(testPodZoneB0, true, unready)
		require.False(t, f.admit(testPodZoneA0).Allowed)
		// The peer recovers, but the cache doesn't see it.
		f.updatePod(testPodZoneB0, false, ready)
		require.NoError(t, markDeleted(f))
	})
	t.Run("sets no deadline", func(t *testing.T) {
		f := newCachedDenialFixture(t, newPartitionBudget(), 1)
		f.updatePod(testPodZoneB0, true, unready)
		require.Error(t, markDeleted(f))
		f.client.ClearActions()
		require.False(t, f.admit(testPodZoneA0).Allowed)
		require.NotEmpty(t, f.client.Actions(), "without a deadline the webhook request goes live")
	})
}
