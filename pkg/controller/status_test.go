package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/status"
)

func TestRolloutController_Snapshot(t *testing.T) {
	tests := map[string]struct {
		statefulSets []runtime.Object
		pods         []runtime.Object
		wantGroups   []status.Group
	}{
		"empty namespace": {
			wantGroups: []status.Group{},
		},
		"complete group": {
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
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseComplete,
				Reason: "",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					},
				},
			}},
		},
		"progressing first zone": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseProgressing,
				Reason: "0 of 3 pods updated",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseProgressing, Reason: "0 of 3 pods updated",
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseWaiting, Reason: "waiting for ingester-zone-a",
					},
				},
			}},
		},
		"paused StatefulSet needing update": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withAnnotations(map[string]string{
					config.RolloutPausedAnnotationKey: config.RolloutPausedAnnotationValue,
				})),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhasePaused,
				Reason: "rollout paused",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, Paused: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhasePaused, Reason: "rollout paused",
					},
				},
			}},
		},
		"paused not-ready zone does not block later zone": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision(), withReplicas(3, 1), withAnnotations(map[string]string{
					config.RolloutPausedAnnotationKey: config.RolloutPausedAnnotationValue,
				})),
				mockStatefulSet("ingester-zone-b", withPrevRevision()),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testPrevRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testPrevRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseProgressing,
				Reason: "0 of 3 pods updated",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 1,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, Paused: true, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhasePaused, Reason: "rollout paused",
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseProgressing, Reason: "0 of 3 pods updated",
					},
				},
			}},
		},
		"invalid update strategy": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", func(sts *v1.StatefulSet) {
					sts.Spec.UpdateStrategy.Type = v1.RollingUpdateStatefulSetStrategyType
				}),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseDegraded,
				Reason: "update strategy is RollingUpdate; OnDelete is required",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3,
						UpdateStrategy: string(v1.RollingUpdateStatefulSetStrategyType),
						Phase:          status.PhaseDegraded, Reason: "update strategy is RollingUpdate; OnDelete is required",
					},
				},
			}},
		},
		"degraded takes priority over multi not-ready wait": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 2), func(sts *v1.StatefulSet) {
					sts.Spec.UpdateStrategy.Type = v1.RollingUpdateStatefulSetStrategyType
				}),
				mockStatefulSet("ingester-zone-b", withReplicas(3, 1)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseDegraded,
				Reason: "update strategy is RollingUpdate; OnDelete is required",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 2,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3,
						UpdateStrategy: string(v1.RollingUpdateStatefulSetStrategyType),
						Phase:          status.PhaseDegraded, Reason: "update strategy is RollingUpdate; OnDelete is required",
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 1,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseWaiting, Reason: "waiting for pods to become Ready",
					},
				},
			}},
		},
		"waiting for readiness after update": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(3, 2)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseWaiting,
				Reason: "waiting for pods to become Ready",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 2,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseWaiting, Reason: "waiting for pods to become Ready",
					},
				},
			}},
		},
		"multiple not-ready sets wait at group level": {
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
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseWaiting,
				Reason: "multiple StatefulSets have not-Ready pods",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 2,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseProgressing, Reason: "0 of 3 pods updated",
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 1,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseProgressing, Reason: "0 of 3 pods updated",
					},
				},
			}},
		},
		"not-ready later zone blocks earlier zone needing update": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withPrevRevision()),
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
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseProgressing,
				Reason: "0 of 3 pods updated",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseWaiting, Reason: "waiting for ingester-zone-b",
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 3, ReadyReplicas: 1,
						CurrentRevision: testPrevRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 0, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseProgressing, Reason: "0 of 3 pods updated",
					},
				},
			}},
		},
		"scale-up with matching revision is waiting not progressing": {
			statefulSets: []runtime.Object{
				mockStatefulSet("ingester-zone-a", withReplicas(5, 3)),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:   "ingester",
				Phase:  status.PhaseWaiting,
				Reason: "waiting for pods to become Ready",
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 5, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, NotReady: true,
						UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase:          status.PhaseWaiting, Reason: "waiting for pods to become Ready",
					},
				},
			}},
		},
		"multi-zone scale-up with matching revisions is not a multi not-ready block": {
			statefulSets: []runtime.Object{
				// Status.Replicas still at old size while Spec desires more: reconcile does not
				// treat this as not-ready (it compares Status.Replicas to ReadyReplicas).
				mockStatefulSet("ingester-zone-a", func(sts *v1.StatefulSet) {
					replicas := int32(5)
					sts.Spec.Replicas = &replicas
					sts.Status.Replicas = 3
					sts.Status.ReadyReplicas = 3
				}),
				mockStatefulSet("ingester-zone-b", func(sts *v1.StatefulSet) {
					replicas := int32(5)
					sts.Spec.Replicas = &replicas
					sts.Status.Replicas = 3
					sts.Status.ReadyReplicas = 3
				}),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-b-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{{
				Name:  "ingester",
				Phase: status.PhaseComplete,
				Members: []status.Member{
					{
						Name: "ingester-zone-a", DesiredReplicas: 5, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					},
					{
						Name: "ingester-zone-b", DesiredReplicas: 5, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					},
				},
			}},
		},
		"groups ordered by name": {
			statefulSets: []runtime.Object{
				mockStatefulSet("store-gateway-zone-a", withLabels(map[string]string{config.RolloutGroupLabelKey: "store-gateway"})),
				mockStatefulSet("ingester-zone-a"),
			},
			pods: []runtime.Object{
				mockStatefulSetPod("store-gateway-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("store-gateway-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("store-gateway-zone-a-2", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-0", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-1", testLastRevisionHash),
				mockStatefulSetPod("ingester-zone-a-2", testLastRevisionHash),
			},
			wantGroups: []status.Group{
				{
					Name:  "ingester",
					Phase: status.PhaseComplete,
					Members: []status.Member{{
						Name: "ingester-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					}},
				},
				{
					Name:  "store-gateway",
					Phase: status.PhaseComplete,
					Members: []status.Member{{
						Name: "store-gateway-zone-a", DesiredReplicas: 3, ReadyReplicas: 3,
						CurrentRevision: testLastRevisionHash, UpdateRevision: testLastRevisionHash,
						UpdatedPods: 3, TotalPods: 3, UpdateStrategy: string(v1.OnDeleteStatefulSetStrategyType),
						Phase: status.PhaseComplete,
					}},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := append(append([]runtime.Object{}, tc.statefulSets...), tc.pods...)
			kubeClient := fake.NewClientset(objs...)
			c := NewRolloutController(kubeClient, nil, nil, nil, testClusterDomain, testNamespace, nil, 5*time.Second, prometheus.NewPedanticRegistry(), log.NewNopLogger(), &mockEvictionController{})
			require.NoError(t, c.Init())
			defer c.Stop()

			snap, err := c.Snapshot(context.Background())
			require.NoError(t, err)
			assert.Equal(t, testNamespace, snap.Namespace)
			assert.False(t, snap.ObservedAt.IsZero())
			assert.Equal(t, tc.wantGroups, snap.Groups)
		})
	}
}
