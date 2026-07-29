package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/grafana/rollout-operator/pkg/controller"
	"github.com/grafana/rollout-operator/pkg/zpdb"
)

const (
	demoNamespace     = "mimir-dev-01"
	demoClusterDomain = "cluster.local."

	demoIngesterOldRev     = "ingester-3b1e44"
	demoIngesterNewRev     = "ingester-7f9c2a"
	demoStoreGatewayOldRev = "store-gateway-aa11bb"
	demoStoreGatewayNewRev = "store-gateway-cc22dd"
	demoCompactorRev       = "compactor-99ee00"
)

type demoEvictionController struct{}

func (demoEvictionController) MarkPodAsDeleted(context.Context, string, string, string, zpdb.MaxUnavailableZeroOverride) error {
	return nil
}

func (demoEvictionController) HasPartitionAwarePdb(*corev1.Pod) (bool, error) {
	return false, nil
}

// newDemoController returns a RolloutController whose informers are seeded with
// mock StatefulSets and Pods covering typical UI states.
func newDemoController() (*controller.RolloutController, error) {
	kubeClient := fake.NewClientset(demoObjects()...)
	c := controller.NewRolloutController(
		kubeClient,
		nil,
		nil,
		nil,
		demoClusterDomain,
		demoNamespace,
		nil,
		5*time.Second,
		prometheus.NewRegistry(),
		log.NewNopLogger(),
		demoEvictionController{},
	)
	if err := c.Init(); err != nil {
		return nil, fmt.Errorf("init demo controller: %w", err)
	}
	return c, nil
}

func demoObjects() []runtime.Object {
	return []runtime.Object{
		// Ingester: zone-a done, zone-b mid-rollout, zone-c not started.
		demoStatefulSet("ingester-zone-a", "ingester", 3, 3, demoIngesterNewRev, demoIngesterNewRev),
		demoStatefulSet("ingester-zone-b", "ingester", 3, 2, demoIngesterOldRev, demoIngesterNewRev),
		demoStatefulSet("ingester-zone-c", "ingester", 3, 3, demoIngesterOldRev, demoIngesterNewRev),
		demoPod("ingester-zone-a-0", demoIngesterNewRev),
		demoPod("ingester-zone-a-1", demoIngesterNewRev),
		demoPod("ingester-zone-a-2", demoIngesterNewRev),
		demoPod("ingester-zone-b-0", demoIngesterNewRev),
		demoPod("ingester-zone-b-1", demoIngesterOldRev),
		demoPod("ingester-zone-b-2", demoIngesterOldRev),
		demoPod("ingester-zone-c-0", demoIngesterOldRev),
		demoPod("ingester-zone-c-1", demoIngesterOldRev),
		demoPod("ingester-zone-c-2", demoIngesterOldRev),

		// Store-gateway: zone-a paused mid-rollout, zone-b continues (pause does not block).
		demoStatefulSet("store-gateway-zone-a", "store-gateway", 2, 2, demoStoreGatewayOldRev, demoStoreGatewayNewRev,
			func(sts *appsv1.StatefulSet) {
				sts.Annotations[config.RolloutPausedAnnotationKey] = config.RolloutPausedAnnotationValue
			},
		),
		demoStatefulSet("store-gateway-zone-b", "store-gateway", 2, 2, demoStoreGatewayOldRev, demoStoreGatewayNewRev),
		demoPod("store-gateway-zone-a-0", demoStoreGatewayOldRev),
		demoPod("store-gateway-zone-a-1", demoStoreGatewayOldRev),
		demoPod("store-gateway-zone-b-0", demoStoreGatewayOldRev),
		demoPod("store-gateway-zone-b-1", demoStoreGatewayOldRev),

		// Compactor: fully rolled out.
		demoStatefulSet("compactor-zone-a", "compactor", 1, 1, demoCompactorRev, demoCompactorRev),
		demoPod("compactor-zone-a-0", demoCompactorRev),
	}
}

func demoStatefulSet(name, group string, replicas, ready int32, currentRev, updateRev string, overrides ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: demoNamespace,
			Labels: map[string]string{
				config.RolloutGroupLabelKey: group,
			},
			Annotations: map[string]string{
				config.RolloutMaxUnavailableAnnotationKey: "1",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Replicas:    &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"name": name,
					},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			Replicas:        replicas,
			ReadyReplicas:   ready,
			CurrentRevision: currentRev,
			UpdateRevision:  updateRev,
		},
	}
	for _, fn := range overrides {
		fn(sts)
	}
	return sts
}

func demoPod(name, revision string) *corev1.Pod {
	stsName := name
	if i := strings.LastIndex(name, "-"); i >= 0 {
		stsName = name[:i]
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: demoNamespace,
			Labels: map[string]string{
				appsv1.ControllerRevisionHashLabelKey: revision,
				"name":                                stsName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "application",
				Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}
