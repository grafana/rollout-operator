package zpdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	k8cache "k8s.io/client-go/tools/cache"

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

const (
	testNamespace = "test-namespace"
)

func newFakeDynamicClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{
				Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
				Version:  ZoneAwarePodDisruptionBudgetsVersion,
				Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
			}: ZoneAwarePodDisruptionBudgetName + "List",
		},
		// No objects passed - will return empty list on query
	)
}

func newConfigObserverTestCase() (*fake.FakeDynamicClient, *configObserver) {
	dynamicClient := newFakeDynamicClient()
	zpdbMetrics := NewMetrics(prometheus.NewRegistry())

	observer := newConfigObserver(dynamicClient, testNamespace, log.NewNopLogger(), zpdbMetrics)
	return dynamicClient, observer
}

func newPDB(name string) *unstructured.Unstructured {
	ret := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable: int64(1),
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: "test-group",
					},
				},
			},
		},
	}
	ret.SetGeneration(int64(1))
	return &ret
}

// TestObserver_NewPdbObserver- basic constructor and life cycle test
func TestObserver_NewPdbObserver(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	require.Equal(t, 0, testutil.CollectAndCount(observer.metrics.ConfigObserverReady))
	require.NoError(t, observer.start())
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigObserverReady.WithLabelValues()))

	select {
	case <-observer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	default:

	}

	observer.stop()

	select {
	case <-observer.stopCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}
}

type watchErrorCapturingInformer struct {
	k8cache.SharedIndexInformer
	handler k8cache.WatchErrorHandler
}

func (i *watchErrorCapturingInformer) SetWatchErrorHandler(handler k8cache.WatchErrorHandler) error {
	i.handler = handler
	return i.SharedIndexInformer.SetWatchErrorHandler(handler)
}

func TestObserver_WatchErrorsPreserveReadiness(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	informer := &watchErrorCapturingInformer{SharedIndexInformer: observer.pdbInformer}
	observer.pdbInformer = informer
	require.NoError(t, observer.start())
	defer observer.stop()
	require.True(t, observer.pdbInformer.HasSynced())
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigObserverReady.WithLabelValues()))

	for _, watchErr := range []error{
		apierrors.NewResourceExpired("resource version expired"),
		apierrors.NewServiceUnavailable("API temporarily unavailable"),
	} {
		informer.handler(nil, watchErr)
		require.True(t, observer.pdbInformer.HasSynced())
		require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigObserverReady.WithLabelValues()))
	}
}

func TestObserver_ReadinessRecoversWithoutObjects(t *testing.T) {
	dynamicClient, observer := newConfigObserverTestCase()
	var unavailable atomic.Bool
	dynamicClient.PrependReactor("list", ZoneAwarePodDisruptionBudgetsNamePlural, func(k8stesting.Action) (bool, runtime.Object, error) {
		if !unavailable.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
			Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
		}, "")
	})

	require.NoError(t, observer.start())
	defer observer.stop()

	unavailable.Store(true)
	observer.updateReadiness()
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigObserverReady.WithLabelValues()))

	unavailable.Store(false)
	observer.updateReadiness()
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigObserverReady.WithLabelValues()))
}

// TestObserver_InvalidObject - tests that no panics occur if an invalid object is passed from the informers
func TestConfigObserver_InvalidObject(t *testing.T) {
	_, observer := newConfigObserverTestCase()
	require.NoError(t, observer.start())
	defer observer.stop()
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))

	invalidObj := "not-a-config"

	observer.onPdbAdded(invalidObj)
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))

	observer.onPdbUpdated(invalidObj, invalidObj)
	require.Equal(t, float64(2), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))

	observer.onPdbDeleted(invalidObj)
	require.Equal(t, float64(3), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))
}

// TestObserver_ZPDBEvents_AddValidZPDB - tests the zpdb configCache being updated on observed config changes
func TestObserver_ZPDBEvents_AddValidZPDB(t *testing.T) {
	dynamicClient, observer := newConfigObserverTestCase()
	require.NoError(t, observer.start())
	defer observer.stop()

	require.Equal(t, observer.pdbCache.size(), 0)
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("invalid")))
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("updated")))
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("deleted")))
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))
	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("delete-ignored")))

	gvr := schema.GroupVersionResource{
		Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  ZoneAwarePodDisruptionBudgetsVersion,
		Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
	}

	// test triggering a new zpdb being created
	_, err := dynamicClient.Resource(gvr).Namespace(testNamespace).Create(context.Background(), newPDB("test-zpdb"), metav1.CreateOptions{})
	require.NoError(t, err)

	task := func() bool {
		return observer.pdbCache.size() > 0
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config configCache has not been initialized")
	require.Equal(t, 1, observer.pdbCache.size())
	require.Contains(t, observer.pdbCache.entries, "test-zpdb")
	require.Equal(t, int64(1), observer.pdbCache.entries["test-zpdb"].generation)
	require.Eventually(t, func() bool {
		return float64(1) == testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("updated"))
	}, time.Second*1, time.Millisecond*10)

	// test triggering a new zpdb being updated
	updatedPdb := newPDB("test-zpdb")
	updatedPdb.SetGeneration(int64(3))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), updatedPdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	task = func() bool {
		observer.pdbCache.lock.RLock()
		defer observer.pdbCache.lock.RUnlock()
		return observer.pdbCache.entries["test-zpdb"].generation == int64(3)
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "zpdb config configCache has not been updated")
	require.Equal(t, 1, observer.pdbCache.size())
	require.Equal(t, int64(3), observer.pdbCache.entries["test-zpdb"].generation)
	require.Equal(t, float64(2), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("updated")))

	// test triggering a stale zpdb update coming in
	stalePdb := newPDB("test-zpdb")
	stalePdb.SetGeneration(int64(2))

	_, err = dynamicClient.Resource(gvr).Namespace(testNamespace).Update(context.Background(), stalePdb, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return observer.pdbCache.size() == 1 }, time.Second*1, time.Millisecond*10)
	require.Equal(t, int64(3), observer.pdbCache.entries["test-zpdb"].generation)
	require.Eventually(t, func() bool {
		return float64(1) == testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored"))
	}, time.Second*1, time.Millisecond*10)

	// test stale delete is ignored - calling handler direct as dynamic client delete is limited
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, 1, observer.pdbCache.size())
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("delete-ignored")))

	stalePdb.SetGeneration(int64(5))
	observer.onPdbDeleted(stalePdb)
	require.Equal(t, 0, observer.pdbCache.size())
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("deleted")))

	require.Equal(t, float64(0), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("invalid")))
	require.Equal(t, float64(2), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("updated")))
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("deleted")))
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("ignored")))
	require.Equal(t, float64(1), testutil.ToFloat64(observer.metrics.ConfigurationsObserved.WithLabelValues("delete-ignored")))
}

func TestObserver_WaitsForMissingCRDByDefault(t *testing.T) {
	dyn, observer := newConfigObserverTestCase()
	listed := make(chan struct{}, 1)
	dyn.PrependReactor("list", ZoneAwarePodDisruptionBudgetsNamePlural, func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case listed <- struct{}{}:
		default:
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: ZoneAwarePodDisruptionBudgetsSpecGroup, Resource: ZoneAwarePodDisruptionBudgetsNamePlural}, "")
	})
	done := make(chan error, 1)
	go func() { done <- observer.start() }()
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
	observer.stop()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not stop")
	}
}

func TestObserver_StopCancelsReadinessProbe(t *testing.T) {
	requested := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	observer := newConfigObserver(client, testNamespace, log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
	done := make(chan struct{})
	go func() { observer.updateReadiness(); close(done) }()
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness probe did not start")
	}
	observer.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness probe did not stop")
	}
}
