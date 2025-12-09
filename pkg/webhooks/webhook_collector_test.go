package webhooks

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestWebhookCollector_CollectsWebhookFailurePolicies(t *testing.T) {
	client := k8sfake.NewClientset()
	collector := NewWebhookCollector(client, "test", log.NewNopLogger())
	require.NoError(t, collector.Start())
	defer collector.Stop()

	ignore := admissionregistrationv1.Ignore
	fail := admissionregistrationv1.Fail

	// Create a validating webhook with no failure policy set - this should default to Fail
	_, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(
		context.Background(),
		&admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "validating-webhook-1",
				Labels: map[string]string{
					"grafana.com/namespace": "test",
				},
			},
			Webhooks: []admissionregistrationv1.ValidatingWebhook{
				{
					Name: "no-failure-policy-set",
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// Create a validating webhook with an Ignore policy
	validatingConfiguration, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(
		context.Background(),
		&admissionregistrationv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "validating-webhook-2",
				Labels: map[string]string{
					"grafana.com/namespace": "test",
				},
			},
			Webhooks: []admissionregistrationv1.ValidatingWebhook{
				{
					Name:          "validating-webhook-with-ignore",
					FailurePolicy: &ignore,
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// Create a mutating webhook with a Fail policy
	mutatingConfiguration, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(
		context.Background(),
		&admissionregistrationv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mutating-webhook-1",
				Labels: map[string]string{
					"grafana.com/namespace": "test",
				},
			},
			Webhooks: []admissionregistrationv1.MutatingWebhook{
				{
					Name:          "mutating-webhook-with-fail",
					FailurePolicy: &fail,
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// Verify metric samples and labels
	expected := `
	# HELP kube_validating_webhook_failure_policy FailurePolicy setting of Kubernetes ValidatingWebhooks and MutatingWebhooks
	# TYPE kube_validating_webhook_failure_policy gauge
    kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 1
	kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 0
	kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="no-failure-policy-set"} 1
	kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="no-failure-policy-set"} 0
	kube_validating_webhook_failure_policy{policy="Fail",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 1
	kube_validating_webhook_failure_policy{policy="Ignore",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 0
	`
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		err = testutil.CollectAndCompare(collector, bytes.NewBufferString(expected), "kube_validating_webhook_failure_policy")
		require.NoError(t, err)
	}, 5*time.Second, 10*time.Millisecond)

	// Update the policy to Ignore and await the collector to report this in the metrics
	mutatingConfiguration.Webhooks[0].FailurePolicy = &ignore
	_, err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(context.Background(), mutatingConfiguration, metav1.UpdateOptions{})
	require.NoError(t, err)

	expected = `
	# HELP kube_validating_webhook_failure_policy FailurePolicy setting of Kubernetes ValidatingWebhooks and MutatingWebhooks
	# TYPE kube_validating_webhook_failure_policy gauge
    kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 1
	kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 0
	kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="no-failure-policy-set"} 1
	kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="no-failure-policy-set"} 0
	kube_validating_webhook_failure_policy{policy="Ignore",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 1
	kube_validating_webhook_failure_policy{policy="Fail",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 0
	`

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		err := testutil.CollectAndCompare(
			collector,
			bytes.NewBufferString(expected),
			"kube_validating_webhook_failure_policy",
		)
		require.NoError(t, err)
	}, 5*time.Second, 10*time.Millisecond)

	// Update the policy to Fail and await the collector to report this in the metrics
	validatingConfiguration.Webhooks[0].FailurePolicy = &fail
	_, err = client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Update(context.Background(), validatingConfiguration, metav1.UpdateOptions{})
	require.NoError(t, err)

	expected = `
	# HELP kube_validating_webhook_failure_policy FailurePolicy setting of Kubernetes ValidatingWebhooks and MutatingWebhooks
	# TYPE kube_validating_webhook_failure_policy gauge
    kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 1
	kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="validating-webhook-with-ignore"} 0
	kube_validating_webhook_failure_policy{policy="Fail",type="ValidatingWebhook",webhook="no-failure-policy-set"} 1
	kube_validating_webhook_failure_policy{policy="Ignore",type="ValidatingWebhook",webhook="no-failure-policy-set"} 0
	kube_validating_webhook_failure_policy{policy="Ignore",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 1
	kube_validating_webhook_failure_policy{policy="Fail",type="MutatingWebhook",webhook="mutating-webhook-with-fail"} 0
	`
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		err := testutil.CollectAndCompare(collector, bytes.NewBufferString(expected), "kube_validating_webhook_failure_policy")
		require.NoError(t, err)
	}, 5*time.Second, 10*time.Millisecond)

	_, err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(
		context.Background(),
		&admissionregistrationv1.MutatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mutating-webhook-2",
				Labels: map[string]string{
					"grafana.com/namespace": "different-namespace",
				},
			},
			Webhooks: []admissionregistrationv1.MutatingWebhook{
				{
					Name:          "mutating-webhook-with-fail",
					FailurePolicy: &fail,
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// The above webhook creation has been ignored - our metrics remain the same
	time.Sleep(5 * time.Second)
	err = testutil.CollectAndCompare(collector, bytes.NewBufferString(expected), "kube_validating_webhook_failure_policy")
	require.NoError(t, err)
}
