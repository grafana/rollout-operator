package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	fakescale "k8s.io/client-go/scale/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/util"
)

const (
	testClusterDomain    = "cluster.local."
	testNamespace        = "test"
	testMaxUnavailable   = 2
	testPrevRevisionHash = "prev-hash"
	testLastRevisionHash = "last-hash"
)

func TestRolloutController_Reconcile(t *testing.T) {
	customResourceGVK := schema.GroupVersionKind{Group: "my.group", Version: "v1", Kind: "CustomResource"}

	tests := map[string]struct {
		statefulSets                      []runtime.Object
		pods                              []runtime.Object
		customResourceScaleSpecReplicas   int
		customResourceScaleStatusReplicas int
		kubePatchErr                      error
		kubeDeleteErr                     error
		kubeUpdateErr                     error
		getScaleErr                       error
		expectedDeletedPods               []string
		expectedUpdatedSets               []string
		expectedPatchedSets               map[string][]string
		expectedPatchedResources          map[string][]string
		expectedErr                       string
		zpdbErrors                        []error
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
					pod.DeletionTimestamp = util.Now()
				}),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash, func(pod *corev1.Pod) {
					pod.DeletionTimestamp = util.Now()
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
					pod.DeletionTimestamp = util.Now()
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
		"should delete pods that needs to be updated - zpdb allows first delete but denies the second": {
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
			expectedDeletedPods: []string{"ingester-zone-b-0"},
			zpdbErrors:          []error{nil, errors.New("zpdb denies eviction request")},
		},
		"should delete pods that needs to be updated - zpdb denies first delete but allows the second": {
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
			expectedDeletedPods: []string{"ingester-zone-b-1"},
			zpdbErrors:          []error{errors.New("zpdb denies eviction request")},
		},
		"should default max unavailable to 1 if set to an invalid value": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), func(sts *v1.StatefulSet) {
					sts.Annotations[config.RolloutMaxUnavailableAnnotationKey] = "xxx"
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
					pod.DeletionTimestamp = util.Now()
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
		"should not update StatefulSet if there are missing pods but not reported by the StatefulSet yet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 3)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
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
					pod.DeletionTimestamp = util.Now()
				}),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
		},
		"should not proceed with the next StatefulSet if there's another one with missing pods but NOT reported by the StatefulSet yet": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 3)),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
		},
		"should ignore any StatefulSet without the rollout group label": {
			statefulSets: []runtime.Object{
				mockStatefulSet("compactor", func(sts *v1.StatefulSet) {
					delete(sts.Labels, config.RolloutGroupLabelKey)
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
		"should not update replicas within minimum time between downscales": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(2, 2),
					withLabels(map[string]string{
						"grafana.com/min-time-between-zones-downscale": "12h",
					}),
					withAnnotations(map[string]string{
						"grafana.com/last-downscale": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
					}),
				),
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withPrevRevision(),
					withLabels(map[string]string{
						"grafana.com/min-time-between-zones-downscale": "12h",
					}),
					withAnnotations(map[string]string{
						"grafana.com/rollout-downscale-leader": "ingester-zone-a",
					}),
				),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			expectedDeletedPods: []string{"ingester-zone-b-0", "ingester-zone-b-1"},
		},
		"should return early and not delete pods if StatefulSet replicas are adjusted": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(2, 2), withLabels(map[string]string{
					"grafana.com/min-time-between-zones-downscale": "12h",
				})),
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withPrevRevision(),
					withLabels(map[string]string{
						"grafana.com/min-time-between-zones-downscale": "12h",
					}),
					withAnnotations(map[string]string{
						"grafana.com/rollout-downscale-leader": "ingester-zone-a",
					}),
				),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			expectedPatchedSets: map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":2}}`}},
		},
		"should not return early and should delete pods if StatefulSet replicas cannot be scaled down": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(2, 2), withLabels(map[string]string{
					"grafana.com/min-time-between-zones-downscale": "12h",
				})),
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withPrevRevision(),
					withLabels(map[string]string{
						"grafana.com/min-time-between-zones-downscale": "12h",
					}),
					withAnnotations(map[string]string{
						"grafana.com/rollout-downscale-leader": "ingester-zone-a",
					}),
				),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			kubePatchErr:        errors.New("cannot patch StatefulSet right now"),
			expectedDeletedPods: []string{"ingester-zone-b-0", "ingester-zone-b-1"}, // Max unavailable is 2 pods
		},
		"should not return early and should delete pods if StatefulSet replicas cannot be scaled up": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 3), withLabels(map[string]string{
					"grafana.com/min-time-between-zones-downscale": "12h",
				})),
				mockStatefulSet("ingester-zone-b", withReplicas(2, 2), withPrevRevision(),
					withLabels(map[string]string{
						"grafana.com/min-time-between-zones-downscale": "12h",
					}),
					withAnnotations(map[string]string{
						"grafana.com/rollout-downscale-leader": "ingester-zone-a",
					}),
				),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
			},
			kubePatchErr:        errors.New("cannot patch StatefulSet right now"),
			expectedDeletedPods: []string{"ingester-zone-b-0", "ingester-zone-b-1"},
		},
		"should return early and scale up statefulset based on reference custom resource": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(2, 2), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   5,
			customResourceScaleStatusReplicas: 2,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":5}}`}},
			expectedPatchedResources:          map[string][]string{"my.group/v1/customresources/test/status": {`{"status":{"replicas":5}}`}},
		},
		"should return early and scale up statefulset based on reference custom resource, but not patch the resource since it's disabled": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(2, 2), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "false",
				})),
			},
			customResourceScaleSpecReplicas:   5,
			customResourceScaleStatusReplicas: 2,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":5}}`}},
			expectedPatchedResources:          nil, // Reference resource is not patched.
		},
		"should return early and scale down statefulset based on reference custom resource": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   2,
			customResourceScaleStatusReplicas: 3,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":2}}`}},
			expectedPatchedResources:          map[string][]string{"my.group/v1/customresources/test/status": {`{"status":{"replicas":2}}`}},
		},
		"should return early and scale down statefulset based on reference custom resource, but not patch the resource since it's disabled": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "false",
				})),
			},
			customResourceScaleSpecReplicas:   2,
			customResourceScaleStatusReplicas: 3,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":2}}`}},
			expectedPatchedResources:          nil, // Reference resource is not patched.
		},
		"should patch scale subresource status.replicas if it doesn't match statefulset": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   3,
			customResourceScaleStatusReplicas: 5,
			expectedPatchedSets:               nil,
			expectedPatchedResources:          map[string][]string{"my.group/v1/customresources/test/status": {`{"status":{"replicas":3}}`}},
		},
		"should not patch scale subresource status.replicas since it's disabled, even though spec.replicas != statefulset.spec.replicas": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "false",
				})),
			},
			customResourceScaleSpecReplicas:   3,
			customResourceScaleStatusReplicas: 5,
			expectedPatchedSets:               nil,
			expectedPatchedResources:          nil, // Reference resource is not patched.
		},
		"should NOT patch scale subresource status.replicas if it already matches statefulset": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   3,
			customResourceScaleStatusReplicas: 3,
		},
		"should not fail if scaling based on custom resource fails due to invalid custom resource": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind + "Invalid",
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			expectedErr: "", // reconciliation continues if scaling fails
		},
		"should not fail if scaling based on custom resource fails due to scale subresource not being available": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			getScaleErr: fmt.Errorf("cannot get scale subresource"),
			expectedErr: "", // reconciliation continues if scaling fails
		},
		"all statefulsets are considered": {
			statefulSets: []runtime.Object{
				// Does NOT have scaling annotations
				mockStatefulSet("ingester-zone-a", withReplicas(4, 4)),
				// Is already scaled.
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
				mockStatefulSet("ingester-zone-c", withReplicas(5, 5), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "true", // Patching of reference resource is explicitly enabled.
				})),
				mockStatefulSet("ingester-zone-d", withReplicas(2, 2), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   5,
			customResourceScaleStatusReplicas: 2,
			expectedPatchedSets:               map[string][]string{"ingester-zone-d": {`{"spec":{"replicas":5}}`}},
			expectedPatchedResources:          map[string][]string{"my.group/v1/customresources/test/status": {`{"status":{"replicas":5}}`, `{"status":{"replicas":5}}`, `{"status":{"replicas":5}}`}},
		},
		"all statefulsets are considered, but only one patches the reference resource": {
			statefulSets: []runtime.Object{
				// Does NOT have scaling annotations
				mockStatefulSet("ingester-zone-a", withReplicas(4, 4)),
				// Is already scaled.
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "false", // Explicitly disabled.
				})),
				mockStatefulSet("ingester-zone-c", withReplicas(5, 5), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
					"grafana.com/rollout-mirror-replicas-from-resource-write-back":  "bad value", // Disabled through wrong value.
				})),
				mockStatefulSet("ingester-zone-d", withReplicas(2, 2), withAnnotations(map[string]string{
					"grafana.com/rollout-mirror-replicas-from-resource-name":        "test",
					"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
					"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
				})),
			},
			customResourceScaleSpecReplicas:   5,
			customResourceScaleStatusReplicas: 2,
			expectedPatchedSets:               map[string][]string{"ingester-zone-d": {`{"spec":{"replicas":5}}`}},
			expectedPatchedResources:          map[string][]string{"my.group/v1/customresources/test/status": {`{"status":{"replicas":5}}`}},
		},
	}

	for testName, testData := range tests {
		t.Run(testName, func(t *testing.T) {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(customResourceGVK, &dummy{})
			restMapper := testrestmapper.TestOnlyStaticRESTMapper(scheme)

			kubeClient := fake.NewSimpleClientset(append(testData.statefulSets, testData.pods...)...)

			// Inject an hook to track all deleted pods or return mocked errors.
			var deletedPods []string
			kubeClient.PrependReactor("delete", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
				if testData.kubeDeleteErr != nil {
					return true, nil, testData.kubeDeleteErr
				}

				switch action.GetResource().Resource {
				case "pods":
					deletedPods = append(deletedPods, action.(ktesting.DeleteAction).GetName())
				}

				return false, nil, nil
			})

			// Inject a hook to track all updated StatefulSets or return mocked errors.
			var updatedSets []*v1.StatefulSet
			var updatedStsNames []string

			kubeClient.PrependReactor("update", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
				if testData.kubeUpdateErr != nil {
					return true, nil, testData.kubeUpdateErr
				}

				switch action.GetResource().Resource {
				case "statefulsets":
					sts := action.(ktesting.UpdateAction).GetObject().(*v1.StatefulSet)

					updatedSets = append(updatedSets, sts)
					updatedStsNames = append(updatedStsNames, sts.Name)
				}

				return false, nil, nil
			})

			patchedStatefulSets := addPatchStatefulsetReactor(kubeClient, testData.kubePatchErr)
			scaleClient := createFakeScaleClient(testData.customResourceScaleSpecReplicas, testData.customResourceScaleStatusReplicas, testData.getScaleErr)
			dynamicClient, patchedStatuses := createFakeDynamicClient()

			// Create the controller and start informers.
			reg := prometheus.NewPedanticRegistry()

			// Pass in a slice of errors. Each eviction request takes and removes from the head and uses this as the eviction response. Once exhausted no evictions will return an error.
			zpdb := &mockEvictionController{nextErrorsIfAny: testData.zpdbErrors}

			c := NewRolloutController(kubeClient, restMapper, scaleClient, dynamicClient, testClusterDomain, testNamespace, nil, 5*time.Second, reg, log.NewNopLogger(), zpdb)
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

			// Assert patched StatefulSets and resources.
			assert.Equal(t, testData.expectedPatchedSets, convertEmptyMapToNil(patchedStatefulSets))
			assert.Equal(t, testData.expectedPatchedResources, convertEmptyMapToNil(patchedStatuses))

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

func TestRolloutController_ReconcileStatefulsetWithDownscaleDelay(t *testing.T) {
	customResourceGVK := schema.GroupVersionKind{Group: "my.group", Version: "v1", Kind: "CustomResource"}

	now := time.Now()

	tests := map[string]struct {
		statefulSets                      []runtime.Object
		customResourceScaleSpecReplicas   int
		customResourceScaleStatusReplicas int
		getScaleErr                       error
		kubePatchErr                      error
		kubeDeleteErr                     error
		kubeUpdateErr                     error
		expectedUpdatedSets               []string
		expectedPatchedSets               map[string][]string
		expectedErr                       string
		httpResponses                     map[string]httpResponse
		expectedHttpRequests              []string
	}{

		"scale down is allowed, if all pods were prepared outside of delay": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,

			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},

			expectedPatchedSets: map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":3}}`}}, // This is the downscale!
		},

		"scale down is not allowed if delay time was not reached yet on all pods": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-30*time.Minute).Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"scale down is not allowed if delay time was not reached on one pod at the end of statefulset": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"limited scale down by 2 replicas is allowed if delay time was reached on some pods at the end of statefulset": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-75*time.Minute).Unix())},
				"POST http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-30*time.Minute).Unix())}, // cannot be scaled down yet, as 1h has not elapsed
				"POST http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-75*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   1, // We want to downscale to single replica
			customResourceScaleStatusReplicas: 5,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":3}}`}}, // Scaledown by 2 replicas (from 5 to 3) is allowed.
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"scale down is not allowed, if POST returns non-200 HTTP status code, even if returned timestamps are outside of delay": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 500, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 500, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"scale down is not allowed, if POST returns error, even if returned timestamps are outside of delay": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {err: fmt.Errorf("network is down"), body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"scale down is not allowed, if response to POST request cannot be parsed": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: "this should be JSON, but isn't"},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"scale down is not allowed for zone-a, but IS allowed for zone-b": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),

				mockStatefulSet("ingester-zone-b", withReplicas(5, 5),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			httpResponses: map[string]httpResponse{
				"POST http://ingester-zone-a-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Unix())},
				"POST http://ingester-zone-a-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Unix())},
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale": {statusCode: 200, body: fmt.Sprintf(`{"timestamp": %d}`, now.Add(-70*time.Minute).Unix())},
			},
			customResourceScaleSpecReplicas:   3, // We want to downscale to 3 replicas only.
			customResourceScaleStatusReplicas: 5,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-a-0.ingester-zone-a.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-a-1.ingester-zone-a.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-a-2.ingester-zone-a.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-a-3.ingester-zone-a.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-a-4.ingester-zone-a.test.svc.cluster.local./prepare-delayed-downscale",

				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-3.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"POST http://ingester-zone-b-4.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},

			expectedPatchedSets: map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":3}}`}}, // This is the downscale!
		},

		"scale up is allowed immediately, cancel any possible previous downscale": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(2, 2),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			customResourceScaleSpecReplicas:   5,
			customResourceScaleStatusReplicas: 2,
			expectedPatchedSets:               map[string][]string{"ingester-zone-b": {`{"spec":{"replicas":5}}`}},
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},

		"if number of replicas is the same, we still cancel any possible previous preparation of delayed downscale": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-b", withReplicas(3, 3),
					withMirrorReplicasAnnotations("test", customResourceGVK),
					withDelayedDownscaleAnnotations(time.Hour, "http://pod/prepare-delayed-downscale")),
			},
			customResourceScaleSpecReplicas:   3,
			customResourceScaleStatusReplicas: 3,
			expectedHttpRequests: []string{
				"DELETE http://ingester-zone-b-0.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-1.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
				"DELETE http://ingester-zone-b-2.ingester-zone-b.test.svc.cluster.local./prepare-delayed-downscale",
			},
		},
	}

	for testName, testData := range tests {
		t.Run(testName, func(t *testing.T) {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(customResourceGVK, &dummy{})
			restMapper := testrestmapper.TestOnlyStaticRESTMapper(scheme)

			kubeClient := fake.NewSimpleClientset(testData.statefulSets...)

			// Inject a hook to track all patched StatefulSets or return mocked errors.
			patchedStsNames := addPatchStatefulsetReactor(kubeClient, testData.kubePatchErr)
			scaleClient := createFakeScaleClient(testData.customResourceScaleSpecReplicas, testData.customResourceScaleStatusReplicas, testData.getScaleErr)

			dynamicClient, _ := createFakeDynamicClient()
			httpClient := &fakeHttpClient{
				responses:       testData.httpResponses,
				defaultResponse: internalErrorResponse,
			}

			// Create the controller and start informers.
			reg := prometheus.NewPedanticRegistry()
			c := NewRolloutController(kubeClient, restMapper, scaleClient, dynamicClient, testClusterDomain, testNamespace, httpClient, 5*time.Second, reg, log.NewNopLogger(), &mockEvictionController{})
			require.NoError(t, c.Init())
			defer c.Stop()

			// Run a reconcile.
			require.NoError(t, c.reconcile(context.Background()))

			// Assert patched StatefulSets.
			assert.Equal(t, testData.expectedPatchedSets, convertEmptyMapToNil(patchedStsNames))
			assert.ElementsMatch(t, testData.expectedHttpRequests, httpClient.requests())
		})
	}
}

func createFakeDynamicClient() (*fakedynamic.FakeDynamicClient, map[string][]string) {
	dynamicClient := &fakedynamic.FakeDynamicClient{}
	patchedStatuses := map[string][]string{}
	dynamicClient.AddReactor("patch", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(ktesting.PatchAction)
		name := path.Join(patchAction.GetResource().Group, patchAction.GetResource().Version, patchAction.GetResource().Resource, patchAction.GetName(), patchAction.GetSubresource())
		if patchedStatuses == nil {
			patchedStatuses = map[string][]string{}
		}
		patchedStatuses[name] = append(patchedStatuses[name], string(action.(ktesting.PatchAction).GetPatch()))
		return true, nil, nil
	})
	return dynamicClient, patchedStatuses
}

// addPatchStatefulsetReactor injects a hook to track all patched StatefulSets or return mocked errors.
func addPatchStatefulsetReactor(fakeKubeClient *fake.Clientset, kubePatchErr error) map[string][]string {
	patchedStsNames := map[string][]string{}
	fakeKubeClient.PrependReactor("patch", "*", func(action ktesting.Action) (handled bool, ret runtime.Object, err error) {
		if kubePatchErr != nil {
			return true, nil, kubePatchErr
		}

		switch action.GetResource().Resource {
		case "statefulsets":
			name := action.(ktesting.PatchAction).GetName()
			patchedStsNames[name] = append(patchedStsNames[name], string(action.(ktesting.PatchAction).GetPatch()))
		}

		return false, nil, nil
	})
	return patchedStsNames
}

func createFakeScaleClient(customResourceScaleSpecReplicas int, customResourceScaleStatusReplicas int, getScaleErr error) *fakescale.FakeScaleClient {
	scaleClient := &fakescale.FakeScaleClient{}
	scaleClient.AddReactor("get", "*", func(rawAction ktesting.Action) (handled bool, ret runtime.Object, err error) {
		if getScaleErr != nil {
			return true, nil, getScaleErr
		}
		action := rawAction.(ktesting.GetAction)
		obj := &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:      action.GetName(),
				Namespace: action.GetNamespace(),
			},
			Spec: autoscalingv1.ScaleSpec{
				Replicas: int32(customResourceScaleSpecReplicas),
			},
			Status: autoscalingv1.ScaleStatus{
				Replicas: int32(customResourceScaleStatusReplicas),
			},
		}
		return true, obj, nil
	})
	return scaleClient
}

func convertEmptyMapToNil[K comparable, V any](m map[K]V) map[K]V {
	if len(m) == 0 {
		return nil
	}
	return m
}

func TestRolloutController_ReconcileShouldDeleteMetricsForDecommissionedRolloutGroups(t *testing.T) {
	ingesters := []runtime.Object{
		mockStatefulSet("ingester-zone-a", func(sts *v1.StatefulSet) { sts.Labels[config.RolloutGroupLabelKey] = "ingester" }),
		mockStatefulSet("ingester-zone-b", func(sts *v1.StatefulSet) { sts.Labels[config.RolloutGroupLabelKey] = "ingester" }),
		mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
		mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
	}

	storeGateways := []runtime.Object{
		mockStatefulSet("store-gateway-zone-a", func(sts *v1.StatefulSet) { sts.Labels[config.RolloutGroupLabelKey] = "store-gateway" }),
		mockStatefulSet("store-gateway-zone-b", func(sts *v1.StatefulSet) { sts.Labels[config.RolloutGroupLabelKey] = "store-gateway" }),
		mockStatefulSetPod("store-gateway-zone-a-0", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-a-1", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-b-0", testLastRevisionHash),
		mockStatefulSetPod("store-gateway-zone-b-1", testLastRevisionHash),
	}

	kubeClient := fake.NewSimpleClientset(append(append([]runtime.Object{}, ingesters...), storeGateways...)...)

	// Create the controller and start informers.
	reg := prometheus.NewPedanticRegistry()
	c := NewRolloutController(kubeClient, nil, nil, nil, testClusterDomain, testNamespace, nil, 5*time.Second, reg, log.NewNopLogger(), &mockEvictionController{})
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
	replicas := int32(3)

	sts := &v1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels: map[string]string{
				config.RolloutGroupLabelKey: "ingester",
			},
			Annotations: map[string]string{
				config.RolloutMaxUnavailableAnnotationKey: strconv.Itoa(testMaxUnavailable),
			},
		},
		Spec: v1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    &replicas,
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
		sts.Spec.Replicas = &totalReplicas
		sts.Status.Replicas = totalReplicas
		sts.Status.ReadyReplicas = readyReplicas
	}
}

func withLabels(labels map[string]string) func(sts *v1.StatefulSet) {
	return func(sts *v1.StatefulSet) {
		existing := sts.GetLabels()
		for k, v := range labels {
			existing[k] = v
		}
		sts.SetLabels(existing)
	}
}

func withAnnotations(annotations map[string]string) func(sts *v1.StatefulSet) {
	return func(sts *v1.StatefulSet) {
		existing := sts.GetAnnotations()
		for k, v := range annotations {
			existing[k] = v
		}
		sts.SetAnnotations(existing)
	}
}

func withMirrorReplicasAnnotations(name string, customResourceGVK schema.GroupVersionKind) func(sts *v1.StatefulSet) {
	return withAnnotations(map[string]string{
		"grafana.com/rollout-mirror-replicas-from-resource-name":        name,
		"grafana.com/rollout-mirror-replicas-from-resource-kind":        customResourceGVK.Kind,
		"grafana.com/rollout-mirror-replicas-from-resource-api-version": customResourceGVK.GroupVersion().String(),
	})
}

func withDelayedDownscaleAnnotations(delay time.Duration, downscaleUrl string) func(sts *v1.StatefulSet) {
	return withAnnotations(map[string]string{
		"grafana.com/rollout-delayed-downscale":             delay.String(),
		"grafana.com/rollout-prepare-delayed-downscale-url": downscaleUrl,
	})
}

type dummy struct {
	runtime.Object
}

type httpResponse struct {
	statusCode int
	body       string
	err        error
}

var internalErrorResponse = httpResponse{
	statusCode: http.StatusInternalServerError,
	body:       "unknown server error",
}

type fakeHttpClient struct {
	recordedRequestsMu sync.Mutex
	recordedRequests   []string

	responses       map[string]httpResponse
	defaultResponse httpResponse
}

func (f *fakeHttpClient) Do(req *http.Request) (resp *http.Response, err error) {
	methodAndURL := fmt.Sprintf("%s %s", req.Method, req.URL.String())

	f.recordedRequestsMu.Lock()
	f.recordedRequests = append(f.recordedRequests, methodAndURL)
	f.recordedRequestsMu.Unlock()

	r, ok := f.responses[methodAndURL]
	if !ok {
		r = f.defaultResponse
	}

	return &http.Response{
		StatusCode: r.statusCode,
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}, r.err
}

func (f *fakeHttpClient) requests() []string {
	f.recordedRequestsMu.Lock()
	defer f.recordedRequestsMu.Unlock()

	return f.recordedRequests
}

type mockEvictionController struct {
	nextErrorsIfAny []error
}

func (m *mockEvictionController) MarkPodAsDeleted(ctx context.Context, namespace string, podName string, source string) error {
	var response error
	if len(m.nextErrorsIfAny) > 0 {
		response = m.nextErrorsIfAny[0]
		m.nextErrorsIfAny = m.nextErrorsIfAny[1:]
	}
	return response
}
