package zpdb

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestK8sClient_PodsNotRunningAndReady_PartitionSnapshot(t *testing.T) {
	owner := newEvictionControllerSts("worker-left")
	other := newEvictionControllerSts("worker-right")
	target := newPod("worker-left-0", owner)
	ready := newPod("worker-right-0", other)
	ready.ResourceVersion = "10"
	setReadyTransitionTime(ready, time.Now().Add(-time.Hour))
	unready := ready.DeepCopy()
	unready.Status.Phase = corev1.PodPending
	unready.ResourceVersion = "11"
	unrelated := newPod("worker-right-1", other)
	unrelated.Status.Phase = corev1.PodPending
	unrelated.ResourceVersion = "12"
	duplicate := newPod("worker-secondary-0", other)
	duplicate.ResourceVersion = "13"
	duplicate.Status.Phase = corev1.PodPending
	justReady := ready.DeepCopy()
	setReadyTransitionTime(justReady, time.Now())
	for _, tc := range []struct {
		name         string
		live         []corev1.Pod
		cached       []corev1.Pod
		replicas     int32
		pending      bool
		delay        time.Duration
		want         zoneStatusResult
		wantDecision string
		fullReads    int
	}{
		{name: "healthy peer and unrelated unready pod", live: []corev1.Pod{*ready, *unrelated}, cached: []corev1.Pod{*ready}, replicas: 2, want: zoneStatusResult{tested: 1}},
		{name: "ready informer predates live disruption", live: []corev1.Pod{*unready}, cached: []corev1.Pod{*ready}, replicas: 1, want: zoneStatusResult{tested: 1, notReady: 1}, wantDecision: "1 pod not ready in partition 0", fullReads: 1},
		{name: "missing informer peer", live: []corev1.Pod{*ready}, replicas: 1, want: zoneStatusResult{tested: 1}, fullReads: 1},
		{name: "uncreated replica outside the partition", live: []corev1.Pod{*ready}, cached: []corev1.Pod{*ready}, replicas: 2, want: zoneStatusResult{tested: 1, unknown: 1}, wantDecision: "1 pod unknown in partition 0"},
		{name: "complete zone without matching partition", live: []corev1.Pod{*unrelated}, replicas: 1, want: zoneStatusResult{}, wantDecision: "1 pod unknown in partition 0"},
		{name: "multiple matching names", live: []corev1.Pod{*ready, *duplicate}, cached: []corev1.Pod{*ready, *duplicate}, replicas: 2, want: zoneStatusResult{tested: 2, notReady: 1}, wantDecision: "1 pod not ready in partition 0"},
		{name: "pending eviction despite current ready status", live: []corev1.Pod{*ready}, cached: []corev1.Pod{*ready}, replicas: 1, pending: true, want: zoneStatusResult{tested: 1, notReady: 1}, wantDecision: "1 pod not ready in partition 0"},
		{name: "cross-zone readiness delay", live: []corev1.Pod{*justReady}, cached: []corev1.Pod{*justReady}, replicas: 1, delay: time.Minute, want: zoneStatusResult{tested: 1, notReady: 1}, wantDecision: "1 pod not ready in partition 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newPodListTransport(t, tc.live)
			c := partitionReadClient(t, transport, tc.cached)
			sts := other.DeepCopy()
			*sts.Spec.Replicas = tc.replicas
			sts.Status.Replicas = tc.replicas
			v := partitionReadValidator(t.Context(), owner, tc.delay)
			if tc.pending {
				v.evictionCache.recordEviction(ready)
			}
			result, err := c.podsNotRunningAndReady(sts, target, v)
			require.NoError(t, err)
			require.Equal(t, tc.want, *result)
			require.NoError(t, v.accumulateResult(sts, result))
			if tc.wantDecision == "" {
				require.NoError(t, v.validate(1))
			} else {
				require.EqualError(t, v.validate(1), tc.wantDecision)
			}
			require.Equal(t, 1, transport.metaReads)
			require.Equal(t, tc.fullReads, transport.fullReads)
			query, err := url.ParseQuery(transport.lastQuery)
			require.NoError(t, err)
			require.Equal(t, url.Values{"labelSelector": {"name=" + other.Name}}, query)
		})
	}
}

func TestK8sClient_PodsNotRunningAndReady_ReplacedPartitionPeer(t *testing.T) {
	owner := newEvictionControllerSts("worker-left")
	other := newEvictionControllerSts("worker-right")
	*other.Spec.Replicas = 2
	other.Status.Replicas = 2
	first := newPod("worker-right-0", other)
	first.ResourceVersion = "18"
	old := newPod("worker-other-0", other)
	old.ResourceVersion = "19"
	replacement := newPod(old.Name, other)
	replacement.ResourceVersion = "20"
	// The first peer also becomes unready between the metadata list and the
	// fallback. Combining partial results from the two snapshots would allow eviction.
	disrupted := first.DeepCopy()
	disrupted.ResourceVersion = "21"
	disrupted.Status.Phase = corev1.PodPending
	transport := newPodListTransport(t, []corev1.Pod{*disrupted, *replacement})
	transport.partial = newPodListTransport(t, []corev1.Pod{*first, *replacement}).partial
	c := partitionReadClient(t, transport, []corev1.Pod{*first, *old})
	v := partitionReadValidator(t.Context(), owner, 0)
	result, err := c.podsNotRunningAndReady(other, newPod("worker-left-0", owner), v)
	require.NoError(t, err)
	require.Equal(t, zoneStatusResult{tested: 2, notReady: 1}, *result)
	require.Equal(t, 1, transport.fullReads)
}

func TestK8sClient_PodsNotRunningAndReady_PartitionReadErrors(t *testing.T) {
	owner := newEvictionControllerSts("worker-left")
	other := newEvictionControllerSts("worker-right")
	target := newPod("worker-left-0", owner)
	peer := newPod("worker-right-0", other)
	peer.ResourceVersion = "20"
	for _, tc := range []struct {
		name                                  string
		metadataStatus, fullStatus, fullReads int
		wantReason                            metav1.StatusReason
	}{
		{name: "unsupported representation retries full list", metadataStatus: http.StatusNotAcceptable, fullReads: 1},
		{name: "metadata permission error propagates", metadataStatus: http.StatusForbidden, wantReason: metav1.StatusReasonForbidden},
		{name: "full list failure after informer miss", fullStatus: http.StatusForbidden, fullReads: 1, wantReason: metav1.StatusReasonForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newPodListTransport(t, []corev1.Pod{*peer})
			transport.metaStatus = tc.metadataStatus
			transport.fullStatus = tc.fullStatus
			c := partitionReadClient(t, transport, nil)
			result, err := c.podsNotRunningAndReady(other, target, partitionReadValidator(t.Context(), owner, 0))
			if tc.wantReason == "" {
				require.NoError(t, err)
				require.Equal(t, 1, result.tested)
			} else {
				require.Error(t, err)
				require.Equal(t, tc.wantReason, apierrors.ReasonForError(err))
				require.Nil(t, result)
			}
			require.Equal(t, tc.fullReads, transport.fullReads)
		})
	}
}
