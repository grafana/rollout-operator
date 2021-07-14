package controller

import (
	"context"
	"regexp"
	"strconv"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
		"should return error if multiple StatefulSets have not-Ready pods": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 2)),
				mockStatefulSet("ingester-zone-b", withPrevRevision(), withReplicas(3, 1)),
			},
			expectedErr: "2 StatefulSets have some not-Ready pods",
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
					ts := metav1.Now()
					pod.DeletionTimestamp = &ts
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
		"should not proceed with the next StatefulSet if there's another one with not-Ready pods": {
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
			c := NewRolloutController(kubeClient, testNamespace, log.NewNopLogger())
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
		})
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
