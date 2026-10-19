package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestReplicaTemplateUpdateFiltersStatus(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"generation": int64(1)},
		"spec":     map[string]interface{}{"replicas": int64(1)},
	}}
	for _, changeSpec := range []bool{false, true} {
		t.Run(map[bool]string{false: "status", true: "spec"}[changeSpec], func(t *testing.T) {
			c := &RolloutController{}
			updated := old.DeepCopy()
			if changeSpec {
				updated.SetGeneration(2)
				require.NoError(t, unstructured.SetNestedField(updated.Object, int64(5), "spec", "replicas"))
			} else {
				require.NoError(t, unstructured.SetNestedField(updated.Object, int64(1), "status", "replicas"))
			}
			c.onReplicaTemplateUpdated(old, updated)
			require.Equal(t, changeSpec, c.shouldReconcile.Load())
		})
	}
}

func TestReplicaTemplateWatchEnqueuesReconcile(t *testing.T) {
	kube := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{replicaTemplateGVR: "ReplicaTemplateList"})
	c := NewRolloutController(kube, nil, nil, nil, dyn, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	c.WatchReplicaTemplates()
	t.Cleanup(c.Stop)
	require.NoError(t, c.Init())
	c.shouldReconcile.Store(false)
	resource := dyn.Resource(replicaTemplateGVR).Namespace(testNamespace)
	obj, err := resource.Create(context.Background(), &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rollout-operator.grafana.com/v1", "kind": "ReplicaTemplate",
		"metadata": map[string]interface{}{"name": "ingester", "namespace": testNamespace, "generation": int64(1)},
		"spec":     map[string]interface{}{"replicas": int64(1)},
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	c.shouldReconcile.Store(false)
	obj.SetGeneration(2)
	require.NoError(t, unstructured.SetNestedField(obj.Object, int64(5), "spec", "replicas"))
	_, err = resource.Update(context.Background(), obj, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	c.shouldReconcile.Store(false)
	require.NoError(t, resource.Delete(context.Background(), obj.GetName(), metav1.DeleteOptions{}))
	require.Eventually(t, c.shouldReconcile.Load, 5*time.Second, 10*time.Millisecond)
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "list" || action.GetVerb() == "watch" {
			require.Equal(t, testNamespace, action.GetNamespace())
		}
	}
}

func TestReplicaTemplateWatchReadinessRecoversFromPermissionErrors(t *testing.T) {
	for _, verb := range []string{"list", "watch"} {
		t.Run(verb, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			kube.Resources = []*metav1.APIResourceList{{GroupVersion: replicaTemplateGVR.GroupVersion().String(), APIResources: []metav1.APIResource{{Name: "replicatemplates"}, {Name: "replicatemplates/scale"}}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{replicaTemplateGVR: "ReplicaTemplateList"})
			var denied atomic.Bool
			denied.Store(true)
			var attempts atomic.Int32
			permissionError := apierrors.NewForbidden(replicaTemplateGVR.GroupResource(), "", nil)
			if verb == "list" {
				dyn.PrependReactor("list", replicaTemplateGVR.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
					attempts.Add(1)
					if denied.Load() {
						return true, nil, permissionError
					}
					return false, nil, nil
				})
			} else {
				dyn.PrependWatchReactor(replicaTemplateGVR.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
					attempts.Add(1)
					if denied.Load() {
						return true, nil, permissionError
					}
					return false, nil, nil
				})
			}
			c := NewRolloutController(kube, newReplicaTemplateTestDiscovery(kube), nil, nil, dyn, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
			c.WatchReplicaTemplates()
			t.Cleanup(c.Stop)
			// Init blocks until the informer syncs, which a denied list prevents.
			go func() { _ = c.Init() }()
			require.Eventually(t, func() bool {
				c.replicaTemplateObserverMu.Lock()
				defer c.replicaTemplateObserverMu.Unlock()
				return c.replicaTemplateAvailable && attempts.Load() > 0
			}, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, float64(0), testutil.ToFloat64(c.replicaTemplateReady))
			denied.Store(false)
			require.Eventually(t, func() bool { return testutil.ToFloat64(c.replicaTemplateReady) == 1 }, 10*time.Second, 10*time.Millisecond)
		})
	}
}

func TestReplicaTemplateReadinessDropsAfterWatchError(t *testing.T) {
	kube := fake.NewSimpleClientset()
	kube.Resources = []*metav1.APIResourceList{{GroupVersion: replicaTemplateGVR.GroupVersion().String(), APIResources: []metav1.APIResource{{Name: "replicatemplates"}, {Name: "replicatemplates/scale"}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{replicaTemplateGVR: "ReplicaTemplateList"})
	initialWatch := watch.NewRaceFreeFake()
	var attempts atomic.Int32
	var denied atomic.Bool
	dyn.PrependWatchReactor(replicaTemplateGVR.Resource, func(k8stesting.Action) (bool, watch.Interface, error) {
		if attempts.Add(1) == 1 {
			return true, initialWatch, nil
		}
		if denied.Load() {
			return true, nil, apierrors.NewForbidden(replicaTemplateGVR.GroupResource(), "", nil)
		}
		return false, nil, nil
	})
	c := NewRolloutController(kube, newReplicaTemplateTestDiscovery(kube), nil, nil, dyn, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	c.WatchReplicaTemplates()
	t.Cleanup(c.Stop)
	require.NoError(t, c.Init())
	require.Eventually(t, func() bool { return testutil.ToFloat64(c.replicaTemplateReady) == 1 }, 5*time.Second, 10*time.Millisecond)
	denied.Store(true)
	initialWatch.Error(&metav1.Status{Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden})
	require.Eventually(t, func() bool { return testutil.ToFloat64(c.replicaTemplateReady) == 0 }, 5*time.Second, 10*time.Millisecond)
	denied.Store(false)
	require.Eventually(t, func() bool { return testutil.ToFloat64(c.replicaTemplateReady) == 1 }, 10*time.Second, 10*time.Millisecond)
}

func TestReplicaTemplateWatchWaitsForCRDByDefault(t *testing.T) {
	kube := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{replicaTemplateGVR: "ReplicaTemplateList"})
	listed := make(chan struct{}, 1)
	dyn.PrependReactor("list", replicaTemplateGVR.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case listed <- struct{}{}:
		default:
		}
		return true, nil, apierrors.NewNotFound(replicaTemplateGVR.GroupResource(), "")
	})
	c := NewRolloutController(kube, nil, nil, nil, dyn, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	c.WatchReplicaTemplates()
	done := make(chan error, 1)
	go func() { done <- c.Init() }()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("informer did not list")
	}
	select {
	case err := <-done:
		t.Fatalf("startup bypassed missing CRD: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	c.Stop()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not stop")
	}
}

func TestReplicaTemplateDiscoveryReadiness(t *testing.T) {
	kube := fake.NewSimpleClientset()
	c := NewRolloutController(kube, newReplicaTemplateTestDiscovery(kube), nil, nil, nil, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	c.updateReplicaTemplateDiscovery()
	require.Equal(t, float64(0), testutil.ToFloat64(c.replicaTemplateReady))

	kube.Resources = []*metav1.APIResourceList{{GroupVersion: replicaTemplateGVR.GroupVersion().String(), APIResources: []metav1.APIResource{{Name: "replicatemplates"}}}}
	c.updateReplicaTemplateDiscovery()
	require.Equal(t, float64(0), testutil.ToFloat64(c.replicaTemplateReady), "scale subresource is required")

	kube.Resources[0].APIResources = append(kube.Resources[0].APIResources, metav1.APIResource{Name: "replicatemplates/scale"})
	c.updateReplicaTemplateDiscovery()
	require.Equal(t, float64(1), testutil.ToFloat64(c.replicaTemplateReady))
}

type replicaTemplateTestDiscovery struct {
	discovery.DiscoveryInterface
	client rest.Interface
}

func (d replicaTemplateTestDiscovery) RESTClient() rest.Interface { return d.client }

func newReplicaTemplateTestDiscovery(kube *fake.Clientset) discovery.DiscoveryInterface {
	client := &restfake.RESTClient{
		NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		GroupVersion:         schema.GroupVersion{Version: "v1"},
		Client: restfake.CreateHTTPClient(func(r *http.Request) (*http.Response, error) {
			resources, err := kube.Discovery().ServerResourcesForGroupVersion(replicaTemplateGVR.GroupVersion().String())
			code := http.StatusOK
			var obj interface{}
			if err != nil {
				code = http.StatusNotFound
				obj = &metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusFailure, Code: int32(code), Message: err.Error()}
			} else {
				resources = resources.DeepCopy()
				resources.TypeMeta = metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}
				obj = resources
			}
			body, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}),
	}
	return replicaTemplateTestDiscovery{DiscoveryInterface: kube.Discovery(), client: client}
}

func TestReplicaTemplateDiscoveryStopsWithController(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requested)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	kube := fake.NewSimpleClientset()
	c := NewRolloutController(kube, discoveryClient, nil, nil, nil, testClusterDomain, testNamespace, NewPodInformerFactory(kube, testNamespace), nil, time.Second, prometheus.NewRegistry(), log.NewNopLogger(), nil)
	done := make(chan struct{})
	go func() { c.updateReplicaTemplateDiscovery(); close(done) }()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery request did not start")
	}
	c.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery request was not canceled")
	}
	require.Equal(t, float64(0), testutil.ToFloat64(c.replicaTemplateReady))
}
