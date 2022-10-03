package controller

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	testNamespace        = "test"
	testMaxUnavailable   = 2
	testPrevRevisionHash = "prev-hash"
	testLastRevisionHash = "last-hash"
)

func TestRolloutController_Reconcile(t *testing.T) {
	tests := map[string]struct {
		statefulSets        []runtime.Object
		pods                []runtime.Object
		expectedDeletedPods []string
		expectedUpdatedSets []string
		expectedErr         string
	}{
		"should return error if some StatefulSet don't have OnDelete update strategy": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", func(sts *v1.StatefulSet) {
					sts.Spec.UpdateStrategy.Type = v1.RollingUpdateStatefulSetStrategyType
				}),
				mockStatefulSet("ingester-zone-b"),
			},
			expectedErr: "ingester-zone-a has RollingUpdate update strategy",
		},
		"should do nothing if multiple StatefulSets have not-Ready pods reported by the StatefulSet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 2)),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), withReplicas(3, 1)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
		},
		"should do nothing if multiple StatefulSets have not-Ready pods but NOT reported by the StatefulSet status yet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 3)),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), withReplicas(3, 3)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = now()
				}),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = now()
				}),
			},
		},
		"should do nothing if multiple StatefulSets have not-Ready pods but ONLY reported by 1 StatefulSet status": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 2)),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), withReplicas(3, 3)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = now()
				}),
			},
		},
		"should do nothing if all pods are updated": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a"),
				mockStatefulSet("ingester-zone-b"),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testLastRevisionHash),
			},
		},
		"should delete pods that needs to be updated, honoring the configured max unavailable": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a"),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			expectedDeletedPods: []string{"ingester-zone-b-0", "ingester-zone-b-1"},
		},
		"should default max unavailable to 1 if set to an invalid value": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), func(sts *v1.StatefulSet) {
					sts.Annotations[RolloutMaxUnavailableAnnotation] = "xxx"
				}),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
			},
			expectedDeletedPods: []string{"ingester-zone-a-0"},
		},
		"should give priority to StatefulSet with not-Ready pods and take not-Ready pods in account when honoring max unavailable": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), func(sts *v1.StatefulSet) {
					sts.Status.Replicas = 3
					sts.Status.ReadyReplicas = 2
				}),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			expectedDeletedPods: []string{"ingester-zone-b-0"}, // Max unavailable = 2 but there's 1 not-Ready pod
		},
		"should do nothing if the number of not-Ready pods >= max unavailable": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), func(sts *v1.StatefulSet) {
					sts.Status.Replicas = 3
					sts.Status.ReadyReplicas = 1
				}),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			expectedDeletedPods: nil, // Max unavailable = 2 and there are 2 not-Ready pods
		},
		"should not delete pods which are already terminating": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = now()
				}),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
			},
			expectedDeletedPods: []string{"ingester-zone-a-1"},
		},
		"should update StatefulSet once all pods have been updated and are Ready": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
			expectedUpdatedSets: []string{"ingester-zone-a"},
		},
		"should not update StatefulSet if all pods have been updated but some are not-Ready": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 1)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
		},
		"should not proceed with the next StatefulSet if there's another one with not-Ready pods reported by the StatefulSet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 2)),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
		},
		"should not proceed with the next StatefulSet if there's another one with not-Ready pods but NOT reported by the StatefulSet yet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 3)),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = now()
				}),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
		},
		"should ignore any StatefulSet without the rollout group label": {
			statefulSets: []runtime.Object{
				mockStatefulSet("compactor", func(sts *v1.StatefulSet) {
					delete(sts.Labels, RolloutGroupLabel)
				}),
				mockStatefulSet("ingester-zone-a"),
				mockStatefulSet("ingester-zone-b"),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("compactor-0", testPrevRevisionHash),
				mockStatefulSetPod("compactor-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testLastRevisionHash),
			},
		},
	}

	for testName, testData := range tests {
		t.Run(testName, func(t *testing.T) {
			kubeClient := fake.NewSimpleClientset(append(testData.statefulSets, testData.pods...)...)

			// Inject an hook to track all deleted pods.
			var deletedPods []string
			kubeClient.PrependReactor("delete", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
				switch action.GetResource().Resource {
				case "pods":
					deletedPods = append(deletedPods, action.(ktesting.DeleteAction).GetName())
				}

				return false, nil, nil
			})

			// Inject an hook to track all updated StatefulSets.
			var updatedSets []*v1.StatefulSet
			var updatedStsNames []string

			kubeClient.PrependReactor("update", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
				switch action.GetResource().Resource {
				case "statefulsets":
					sts := action.(ktesting.UpdateAction).GetObject().(*v1.StatefulSet)

					updatedSets = append(updatedSets, sts)
					updatedStsNames = append(updatedStsNames, sts.Name)
				}

				return false, nil, nil
			})

			// Create the controller and start informers.
			reg := prometheus.NewPedanticRegistry()
			c := NewRolloutController(kubeClient, testNamespace, reg, log.NewNopLogger())
			require.NoError(t, c.Init())
			defer c.Stop()

			// Run a reconcile.
			err := c.reconcile(context.Background())
			if testData.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), testData.expectedErr)
			} else {
				require.NoError(t, err)
			}

			// Assert deleted pods.
			assert.Equal(t, testData.expectedDeletedPods, deletedPods)

			// Assert updated StatefulSets.
			assert.Equal(t, testData.expectedUpdatedSets, updatedStsNames)
			for _, sts := range updatedSets {
				// We expect the update hash to be stored as current revision when the StatefulSet is updated.
				assert.Equal(t, testLastRevisionHash, sts.Status.CurrentRevision)
				assert.Equal(t, testLastRevisionHash, sts.Status.UpdateRevision)
			}

			// Assert on metrics.
			expectedFailures := 0
			if testData.expectedErr != "" {
				expectedFailures = 1
			}

			assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(fmt.Sprintf(`
				# HELP rollout_operator_group_reconciles_total Total number of reconciles started for a specific rollout group.
				# TYPE rollout_operator_group_reconciles_total counter
				rollout_operator_group_reconciles_total{rollout_group="ingester"} 1

				# HELP rollout_operator_group_reconciles_failed_total Total number of reconciles failed for a specific rollout group.
				# TYPE rollout_operator_group_reconciles_failed_total counter
				rollout_operator_group_reconciles_failed_total{rollout_group="ingester"} %d
			`, expectedFailures)),
				"rollout_operator_group_reconciles_total",
				"rollout_operator_group_reconciles_failed_total"))
		})
	}
}

func TestRolloutController_ReconcileShouldDeleteMetricsForDecommissionedRolloutGroups(t *testing.T) {
	ingesters := []runtime.Object{
		mockStatefulSet("ingester-zone-a", func(sts *v1.StatefulSet) { sts.ObjectMeta.Labels[RolloutGroupLabel] = "ingester" }),
		mockStatefulSet("ingester-zone-b", func(sts *v1.StatefulSet) { sts.ObjectMeta.Labels[RolloutGroupLabel] = "ingester" }),
		mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
	}

	storeGateways := []runtime.Object{
		mockStatefulSet("store-gateway-zone-a", func(sts *v1.StatefulSet) { sts.ObjectMeta.Labels[RolloutGroupLabel] = "store-gateway" }),
		mockStatefulSet("store-gateway-zone-b", func(sts *v1.StatefulSet) { sts.ObjectMeta.Labels[RolloutGroupLabel] = "store-gateway" }),
		mockStatefulSetPod("store-gateway-zone-a-0", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-a-1", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-b-0", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-b-1", testLastRevisionHash),
	}

	kubeClient := fake.NewSimpleClientset(append(append([]runtime.Object{}, ingesters...), storeGateways...)...)

	// Create the controller and start informers.
	reg := prometheus.NewPedanticRegistry()
	c := NewRolloutController(kubeClient, testNamespace, reg, log.NewNopLogger())
	require.NoError(t, c.Init())
	defer c.Stop()

	// Run a reconcile. We expect it to do nothing.
	{
		require.NoError(t, c.reconcile(context.Background()))
		assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
			# HELP rollout_operator_group_reconciles_total Total number of reconciles started for a specific rollout group.
			# TYPE rollout_operator_group_reconciles_total counter
			rollout_operator_group_reconciles_total{rollout_group="ingester"} 1
			rollout_operator_group_reconciles_total{rollout_group="store-gateway"} 1

			# HELP rollout_operator_group_reconciles_failed_total Total number of reconciles failed for a specific rollout group.
			# TYPE rollout_operator_group_reconciles_failed_total counter
			rollout_operator_group_reconciles_failed_total{rollout_group="ingester"} 0
			rollout_operator_group_reconciles_failed_total{rollout_group="store-gateway"} 0
		`), "rollout_operator_group_reconciles_total", "rollout_operator_group_reconciles_failed_total"))
	}

	// Delete store-gateways and reconcile again. We expect store-gateways metrics to be removed too.
	{
		sets, err := c.listStatefulSetsWithRolloutGroup()
		require.NoError(t, err)
		require.Len(t, sets, 4) // Pre-condition check.

		// Delete store-gateway StatefulSets from mocked tracker.
		for _, sts := range sets {
			if strings.Contains(sts.Name, "store-gateway") {
				resource := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
				require.NoError(t, kubeClient.Tracker().Delete(resource, sts.Namespace, sts.Name))
			}
		}

		// Since the listeners are updated asynchronously, we have to wait until they're updated.
		require.Eventually(t, func() bool {
			sets, err := c.listStatefulSetsWithRolloutGroup()
			return err == nil && len(sets) == 2
		}, 5*time.Second, 100*time.Millisecond)

		require.NoError(t, c.reconcile(context.Background()))

		assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
			# HELP rollout_operator_group_reconciles_total Total number of reconciles started for a specific rollout group.
			# TYPE rollout_operator_group_reconciles_total counter
			rollout_operator_group_reconciles_total{rollout_group="ingester"} 2

			# HELP rollout_operator_group_reconciles_failed_total Total number of reconciles failed for a specific rollout group.
			# TYPE rollout_operator_group_reconciles_failed_total counter
			rollout_operator_group_reconciles_failed_total{rollout_group="ingester"} 0
		`), "rollout_operator_group_reconciles_total", "rollout_operator_group_reconciles_failed_total"))
	}
}

func mockStatefulSet(name string, overrides ...func(sts *v1.StatefulSet)) *v1.StatefulSet {
	sts := &v1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				RolloutGroupLabel: "ingester",
			},
			Annotations: map[string]string{
				RolloutMaxUnavailableAnnotation: strconv.Itoa(testMaxUnavailable),
			},
		},
		Spec: v1.StatefulSetSpec{
			UpdateStrategy: v1.StatefulSetUpdateStrategy{
				Type: v1.OnDeleteStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"name": name,
					},
				},
			},
		},
		Status: v1.StatefulSetStatus{
			Replicas:        3,
			ReadyReplicas:   3,
			CurrentRevision: testLastRevisionHash,
			UpdateRevision:  testLastRevisionHash,
		},
	}

	for _, fn := range overrides {
		fn(sts)
	}

	return sts
}

func mockStatefulSetPod(name, revision string, overrides ...func(pod *corev1.Pod)) *corev1.Pod {
	// The StatefulSet name is the pod name without the final "-<number>".
	statefulSetName := regexp.MustCompile(`-\d+$`).ReplaceAllString(name, "")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				v1.ControllerRevisionHashLabelKey: revision,
				"name":                            statefulSetName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "application",
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					Ready: true,
				},
			},
		},
	}

	for _, fn := range overrides {
		fn(pod)
	}

	return pod
}

func withPrevRevision() func(sts *v1.StatefulSet) {
	return func(sts *v1.StatefulSet) {
		sts.Status.CurrentRevision = testPrevRevisionHash
	}
}

func withReplicas(totalReplicas, readyReplicas int32) func(sts *v1.StatefulSet) {
	return func(sts *v1.StatefulSet) {
		sts.Status.Replicas = totalReplicas
		sts.Status.ReadyReplicas = readyReplicas
	}
}
