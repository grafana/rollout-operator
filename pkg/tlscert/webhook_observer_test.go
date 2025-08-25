package tlscert

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

type mockLogger struct{}

func (m mockLogger) Log(keyvals ...interface{}) error {
	return nil
}

func TestWebhookObserver_lifeCycle(t *testing.T) {
	logger := &mockLogger{}
	observer := NewWebhookObserver(k8sfake.NewClientset(), "test-namespace", logger)

	listener := &WebhookConfigurationListener{}
	require.NoError(t, observer.Init(listener))

	// Ensure that stopCh has been opened
	select {
	case <-observer.stopCh:
		t.Fatal("stopCh should not be closed initially")
	default:
		// Expected - channel is open
	}

	observer.Stop()

	// Ensure that stopCh has been closed
	select {
	case <-observer.stopCh:
		// Expected - channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stopCh should be closed after Stop()")
	}
}

// TestWebhookObserver_ListenerInvoked tests that the WebhookObserver correctly invokes the registered callback when a new validating webhook configuration or mutating webhook configuration is observed.
func TestWebhookObserver_ListenerInvoked(t *testing.T) {
	logger := &mockLogger{}
	client := k8sfake.NewClientset()
	observer := NewWebhookObserver(client, "test-namespace", logger)

	// create a listener which simply adds the observed webhooks to these slices
	observedValidatingWebhooks := make([]*admissionregistrationv1.ValidatingWebhookConfiguration, 0)
	observedMutatingWebhooks := make([]*admissionregistrationv1.MutatingWebhookConfiguration, 0)

	listener := &WebhookConfigurationListener{
		OnValidatingWebhookConfiguration: func(webhook *admissionregistrationv1.ValidatingWebhookConfiguration) error {
			observedValidatingWebhooks = append(observedValidatingWebhooks, webhook)
			return nil
		},
		OnMutatingWebhookConfiguration: func(webhook *admissionregistrationv1.MutatingWebhookConfiguration) error {
			observedMutatingWebhooks = append(observedMutatingWebhooks, webhook)
			return nil
		},
	}
	require.NoError(t, observer.Init(listener))
	defer observer.Stop()

	require.Empty(t, observedValidatingWebhooks)
	require.Empty(t, observedMutatingWebhooks)

	validatingWebhook, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(context.Background(), validatingWebhookConfiguration("validating-webhook"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, validatingWebhook)

	// wait for the informer to be aware of this create
	task := func() bool {
		return len(observedValidatingWebhooks) == 1
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "ValidatingWebhookConfiguration should have 1 ValidatingWebhookConfiguration")
	require.Equal(t, "validating-webhook", observedValidatingWebhooks[0].Name)
	require.Empty(t, observedMutatingWebhooks)

	mutatingWebhook, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(context.Background(), mutatingWebhookConfiguration("mutating-webhook"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, mutatingWebhook)

	// wait for the informer to be aware of this create
	task = func() bool {
		return len(observedMutatingWebhooks) == 1
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "MutatingWebhookConfiguration should have 1 MutatingWebhookConfiguration")
	require.Equal(t, "mutating-webhook", observedMutatingWebhooks[0].Name)

	// modify our validating webhook
	validatingWebhook.SetLabels(map[string]string{"foo": "bar"})
	data, err := json.Marshal(validatingWebhook)
	require.NoError(t, err)
	_, err = client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Patch(context.Background(), validatingWebhook.GetName(), types.MergePatchType, data, metav1.PatchOptions{})
	require.NoError(t, err)

	// wait for the informer to be aware of this create
	task = func() bool {
		return len(observedValidatingWebhooks) == 2
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "ValidatingWebhookConfiguration should have 2 ValidatingWebhookConfigurations")

	// modify our mutating webhook
	mutatingWebhook.SetLabels(map[string]string{"foo": "bar"})
	data, err = json.Marshal(mutatingWebhook)
	require.NoError(t, err)
	_, err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Patch(context.Background(), mutatingWebhook.GetName(), types.MergePatchType, data, metav1.PatchOptions{})
	require.NoError(t, err)

	// wait for the informer to be aware of this create
	task = func() bool {
		return len(observedMutatingWebhooks) == 2
	}
	require.Eventually(t, task, time.Second*5, time.Millisecond*10, "MutatingWebhookConfiguration should have 2 MutatingWebhookConfigurations")
}

// validatingWebhookConfiguration returns a new ValidatingWebhookConfiguration with only it's name set
func validatingWebhookConfiguration(name string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// mutatingWebhookConfiguration returns a new MutatingWebhookConfiguration with only it's name set
func mutatingWebhookConfiguration(name string) *admissionregistrationv1.MutatingWebhookConfiguration {
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}
