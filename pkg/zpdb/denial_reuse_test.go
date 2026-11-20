package zpdb

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
)

func TestEvictionController_HandlePodEvictionRequest_ReusesBudgetDenial(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("partitioned=%t", partitioned), func(t *testing.T) {
			c, client, req, peer := newDenialFixture(t, 1, partitioned)
			now := time.Now()
			c.now = func() time.Time { return now }
			admit := func() *admissionv1.AdmissionResponse {
				return c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone())
			}
			denied := admit()
			require.EqualValues(t, http.StatusTooManyRequests, denied.Result.Code)

			// Recover the peer. A retry must use its own UID and must not extend the original deadline.
			peer.Status.Phase = corev1.PodRunning
			_, err := client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
			require.NoError(t, err)
			client.ClearActions()
			req.Request.UID = "retry"
			now = now.Add(denialReuseWindow - time.Millisecond)
			retry := admit()
			require.Equal(t, req.Request.UID, retry.UID)
			require.Equal(t, denied.Result, retry.Result)
			require.Equal(t, 1., testutil.ToFloat64(c.metrics.ReusedDenials))
			require.Equal(t, 2., testutil.ToFloat64(c.metrics.EvictionRequests.WithLabelValues("denied", "429")))
			for _, action := range client.Actions() {
				require.False(t, action.Matches("list", "pods"), "cached denial listed peer pods")
			}
			now = now.Add(time.Millisecond)
			require.True(t, admit().Allowed)
		})
	}
}

func TestEvictionController_HandlePodEvictionRequest_QueuedRequest(t *testing.T) {
	c, client, req, peer := newDenialFixture(t, 1, true)
	now := time.Now()
	c.now = func() time.Time { return now }
	admit := func() *admissionv1.AdmissionResponse {
		return c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone())
	}
	require.False(t, admit().Allowed)
	lock := c.findLock(rolloutGroupValue + "-rollout")
	lock.Lock()
	gotTarget := make(chan struct{}, 1)
	client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case gotTarget <- struct{}{}:
		default:
		}
		return false, nil, nil
	})
	response := make(chan *admissionv1.AdmissionResponse, 1)
	go func() { response <- admit() }()
	<-gotTarget
	now = now.Add(denialReuseWindow)
	lock.Unlock()
	require.False(t, (<-response).Allowed)
	require.Zero(t, testutil.ToFloat64(c.metrics.ReusedDenials))

	// The queued request reached a fresh denial, but its reuse window had already elapsed.
	peer.Status.Phase = corev1.PodRunning
	_, err := client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.True(t, admit().Allowed)
}

func TestEvictionController_HandlePodEvictionRequest_DryRun(t *testing.T) {
	for _, form := range []string{"admission flag", "eviction body"} {
		t.Run(form, func(t *testing.T) {
			c, client, req, peer := newDenialFixture(t, 1, true)
			admit := func(r admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
				return c.HandlePodEvictionRequest(t.Context(), r, NewMaxUnavailableZeroOverrideNone())
			}
			require.False(t, admit(req).Allowed)
			peer.Status.Phase = corev1.PodRunning
			_, err := client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
			require.NoError(t, err)
			probe := req.DeepCopy()
			flag := form == "admission flag"
			probe.Request.DryRun = &flag
			if !flag {
				probe.Request.Object.Raw = []byte(`{"deleteOptions":{"dryRun":["All"]}}`)
			}
			// The dry run must see recovery even with a live denial entry for the target.
			require.True(t, admit(*probe).Allowed)
			// A probe cannot reserve the target's slot and block the actual peer eviction.
			require.True(t, admit(createBasicEvictionAdmissionReview(peer.Name, testNamespace)).Allowed)
		})
	}
}

func TestEvictionController_HandlePodEvictionRequest_ProbeDenialDoesNotBlockAdmission(t *testing.T) {
	c, client, req, peer := newDenialFixture(t, 1, true)
	dryRun := true
	req.Request.DryRun = &dryRun
	denied := c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone())
	require.EqualValues(t, http.StatusTooManyRequests, denied.Result.Code)

	// Both kinds of probe can see the disruption, but neither may publish a reusable denial.
	require.Error(t, c.MarkPodAsDeleted(t.Context(), testNamespace, req.Request.Name, "controller", NewMaxUnavailableZeroOverrideNone()))
	peer.Status.Phase = corev1.PodRunning
	_, err := client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
	require.NoError(t, err)
	req.Request.DryRun = nil
	require.True(t, c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone()).Allowed)
}

func TestEvictionController_MarkPodAsDeleted_BypassesDenial(t *testing.T) {
	c, client, req, peer := newDenialFixture(t, 1, true)
	require.False(t, c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone()).Allowed)
	peer.Status.Phase = corev1.PodRunning
	_, err := client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, c.MarkPodAsDeleted(t.Context(), testNamespace, req.Request.Name, "controller", NewMaxUnavailableZeroOverrideNone()))
	require.False(t, c.HandlePodEvictionRequest(t.Context(), createBasicEvictionAdmissionReview(peer.Name, testNamespace), NewMaxUnavailableZeroOverrideNone()).Allowed)
}

func TestEvictionController_HandlePodEvictionRequest_DenialIdentity(t *testing.T) {
	for _, change := range []string{"recreated target", "replaced budget", "percentage threshold"} {
		t.Run(change, func(t *testing.T) {
			c, client, req, peer := newDenialFixture(t, 1, false)
			// Use a percentage budget so a scale change can alter the threshold without a new config.
			target, err := client.CoreV1().Pods(testNamespace).Get(t.Context(), req.Request.Name, metav1.GetOptions{})
			require.NoError(t, err)
			cfg, err := c.cfgObserver.pdbCache.find(target)
			require.NoError(t, err)
			pct := *cfg
			pct.generation++
			pct.maxUnavailable = 0
			pct.maxUnavailablePercentage = 100
			c.cfgObserver.pdbCache.addOrUpdate(&pct)
			admit := func() bool {
				return c.HandlePodEvictionRequest(t.Context(), req, NewMaxUnavailableZeroOverrideNone()).Allowed
			}
			require.False(t, admit())
			peer.Status.Phase = corev1.PodRunning
			_, err = client.CoreV1().Pods(testNamespace).UpdateStatus(t.Context(), peer, metav1.UpdateOptions{})
			require.NoError(t, err)
			switch change {
			case "recreated target":
				require.NoError(t, client.CoreV1().Pods(testNamespace).Delete(t.Context(), target.Name, metav1.DeleteOptions{}))
				target.UID = types.UID("replacement")
				target.ResourceVersion = ""
				_, err = client.CoreV1().Pods(testNamespace).Create(t.Context(), target, metav1.CreateOptions{})
			case "replaced budget":
				c.cfgObserver.pdbCache.delete(pct.generation, pct.name)
				replacement := pct
				c.cfgObserver.pdbCache.addOrUpdate(&replacement)
			case "percentage threshold":
				sts, getErr := client.AppsV1().StatefulSets(testNamespace).Get(t.Context(), target.OwnerReferences[0].Name, metav1.GetOptions{})
				require.NoError(t, getErr)
				*sts.Spec.Replicas = 2
				sts.Status.Replicas = 2
				_, err = client.AppsV1().StatefulSets(testNamespace).Update(t.Context(), sts, metav1.UpdateOptions{})
			}
			require.NoError(t, err)
			require.True(t, admit())
		})
	}
}
