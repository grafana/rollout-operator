package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/status"
)

func blockerTestController(t *testing.T, sts *v1.StatefulSet, pods []*corev1.Pod, eviction *mockEvictionController) *RolloutController {
	t.Helper()
	objects := []runtime.Object{sts}
	for _, pod := range pods {
		objects = append(objects, pod)
	}
	client := fake.NewClientset(objects...)
	c := NewRolloutController(client, nil, nil, nil, testClusterDomain, testNamespace, NewPodInformerFactory(client, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), eviction)
	require.NoError(t, c.statefulSetsInformer.GetStore().Add(sts))
	for _, pod := range pods {
		require.NoError(t, c.podsInformer.GetStore().Add(pod))
	}
	return c
}

func TestRolloutBlockersFromDeletionAttempts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ready       int32
		podOverride func(*corev1.Pod)
		denial      error
		want        string
		deleted     bool
	}{
		{name: "budget exhausted", ready: 0, want: "maxUnavailable budget exhausted: 1 unavailable, limit 1"},
		{name: "terminating", ready: 1, podOverride: func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now }, want: "waiting for pod to terminate"},
		{name: "ZPDB denial", ready: 1, denial: errors.New("zone-b has unavailable pods"), want: "ZPDB denied deletion: zone-b has unavailable pods"},
		{name: "stuck pod bypasses exhausted budget", ready: 0, podOverride: withCrashLoopBackOff(), deleted: true},
		{name: "successful deletion", ready: 1, deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sts := mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(1, tc.ready))
			sts.Annotations[config.RolloutMaxUnavailableAnnotationKey] = "1"
			pod := mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash)
			pod.UID = "original-pod"
			if tc.podOverride != nil {
				tc.podOverride(pod)
			}
			eviction := &mockEvictionController{}
			if tc.denial != nil {
				eviction.nextErrorsIfAny = []error{tc.denial}
			}
			c := blockerTestController(t, sts, []*corev1.Pod{pod}, eviction)
			_, err := c.updateStatefulSetPods(context.Background(), sts)
			require.NoError(t, err)
			blockers := c.currentRolloutBlockers(sts)
			if tc.want == "" {
				require.Empty(t, blockers)
			} else {
				require.Len(t, blockers, 1)
				require.Equal(t, tc.want, blockers[0].Reason)
				require.WithinDuration(t, time.Now(), blockers[0].ObservedAt, time.Second)
				if tc.denial != nil || tc.podOverride != nil {
					require.Equal(t, pod.Name, blockers[0].Pod)
				}
			}
			var deleted bool
			for _, action := range c.kubeClient.(*fake.Clientset).Actions() {
				if action.GetVerb() == "delete" {
					deleted = true
				}
			}
			require.Equal(t, tc.deleted, deleted)
			before := len(c.kubeClient.(*fake.Clientset).Actions())
			snap, err := c.Snapshot(context.Background())
			require.NoError(t, err)
			require.Equal(t, blockers, snap.Groups[0].Members[0].Blockers)
			require.Len(t, c.kubeClient.(*fake.Clientset).Actions(), before, "status requests must not perform API calls")
		})
	}
}

func TestRolloutBlockersLifecycle(t *testing.T) {
	sts := mockStatefulSet("ingester-zone-a", withPrevRevision())
	sts.UID = "original-set"
	pod := mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash)
	pod.UID = "original-pod"
	c := blockerTestController(t, sts, []*corev1.Pod{pod}, &mockEvictionController{})
	blockers := []status.Blocker{{Pod: pod.Name, PodUID: pod.UID, Reason: "ZPDB denied deletion: zone-b unavailable"}}
	c.recordRolloutBlockers(sts, blockers)
	require.Len(t, c.currentRolloutBlockers(sts), 1)
	for _, mutate := range []func(*v1.StatefulSet){
		func(s *v1.StatefulSet) { s.UID = "replacement" },
		func(s *v1.StatefulSet) { s.Generation++ },
		func(s *v1.StatefulSet) { s.Status.UpdateRevision = "new-rollout" },
	} {
		changed := sts.DeepCopy()
		mutate(changed)
		require.Empty(t, c.currentRolloutBlockers(changed))
	}
	replacement := pod.DeepCopy()
	replacement.UID = "replacement-pod"
	require.NoError(t, c.podsInformer.GetStore().Update(replacement))
	require.Empty(t, c.currentRolloutBlockers(sts))
	replacement = pod.DeepCopy()
	replacement.Labels[v1.ControllerRevisionHashLabelKey] = sts.Status.UpdateRevision
	require.NoError(t, c.podsInformer.GetStore().Update(replacement))
	require.Empty(t, c.currentRolloutBlockers(sts))
	require.NoError(t, c.podsInformer.GetStore().Delete(replacement))
	require.Empty(t, c.currentRolloutBlockers(sts))
	c.clearRolloutBlockers([]*v1.StatefulSet{sts})
	require.Empty(t, c.rolloutBlockers)
	c.recordRolloutBlockers(sts, blockers)
	c.pruneRolloutBlockers(nil)
	require.Empty(t, c.rolloutBlockers)
	c.recordRolloutBlockers(sts, blockers)
	c.recordRolloutBlockers(sts, nil)
	require.Empty(t, c.rolloutBlockers)
}

func TestRolloutBlockersConcurrentReaders(t *testing.T) {
	sts := mockStatefulSet("ingester-zone-a", withPrevRevision())
	c := blockerTestController(t, sts, nil, &mockEvictionController{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				c.recordRolloutBlockers(sts, []status.Blocker{{Reason: "budget exhausted"}})
				c.currentRolloutBlockers(sts)
				c.clearRolloutBlockers([]*v1.StatefulSet{sts})
			}
		})
	}
	wg.Wait()
}

func TestRolloutBlockersStatusUpdateFailure(t *testing.T) {
	sts := mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(1, 1))
	pod := mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash)
	c := blockerTestController(t, sts, []*corev1.Pod{pod}, &mockEvictionController{})
	c.kubeClient.(*fake.Clientset).PrependReactor("update", "statefulsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("status update failed")
	})
	_, err := c.updateStatefulSetPods(context.Background(), sts)
	require.ErrorContains(t, err, "status update failed")
	require.Empty(t, c.currentRolloutBlockers(sts))
}
