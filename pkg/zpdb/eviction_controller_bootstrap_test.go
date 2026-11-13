package zpdb

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestEvictionsWaitForInitialCaches(t *testing.T) {
	kubeClient := fake.NewClientset(newPodNoOwner(testPodZoneA0))
	dynamicClient := newFakeDynamicClient()
	var available atomic.Bool
	dynamicClient.PrependReactor("list", ZoneAwarePodDisruptionBudgetsNamePlural, func(k8stesting.Action) (bool, runtime.Object, error) {
		if !available.Load() {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: ZoneAwarePodDisruptionBudgetsSpecGroup, Resource: ZoneAwarePodDisruptionBudgetsNamePlural}, "")
		}
		return false, nil, nil
	})
	c := NewEvictionController(kubeClient, dynamicClient, testNamespace, newTestPodsFactory(kubeClient), log.NewNopLogger(), NewMetrics(prometheus.NewRegistry()))
	t.Cleanup(c.Stop)
	request := createBasicEvictionAdmissionReview(testPodZoneA0, testNamespace)
	response := c.HandlePodEvictionRequest(t.Context(), request, NewMaxUnavailableZeroOverrideNone())
	require.False(t, response.Allowed)
	require.EqualValues(t, http.StatusServiceUnavailable, response.Result.Code)
	require.Empty(t, kubeClient.Actions())
	require.ErrorContains(t, c.MarkPodAsDeleted(t.Context(), testNamespace, testPodZoneA0, "test", NewMaxUnavailableZeroOverrideNone()), "caches are not initialized")

	c.AllowMissingCRDs()
	require.NoError(t, c.Start())
	response = c.HandlePodEvictionRequest(t.Context(), request, NewMaxUnavailableZeroOverrideNone())
	require.False(t, response.Allowed)
	require.EqualValues(t, http.StatusServiceUnavailable, response.Result.Code)

	available.Store(true)
	require.Eventually(t, func() bool {
		return c.HandlePodEvictionRequest(t.Context(), request, NewMaxUnavailableZeroOverrideNone()).Allowed
	}, 10*time.Second, 10*time.Millisecond)
}
