package tlscert

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	require.Equal(t, 0, len(observedValidatingWebhooks))
	require.Equal(t, 0, len(observedMutatingWebhooks))

	_, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(context.Background(), validatingWebhookConfiguration("validating-webhook"), metav1.CreateOptions{})
	require.NoError(t, err)

	// wait for the informer to be aware of this create
	task := func() bool {
		return len(observedValidatingWebhooks) == 1
	}
	await(t, task, "ValidatingWebhookConfiguration should have 1 ValidatingWebhookConfiguration")
	require.Equal(t, 1, len(observedValidatingWebhooks))
	require.Equal(t, "validating-webhook", observedValidatingWebhooks[0].Name)
	require.Equal(t, 0, len(observedMutatingWebhooks))

	_, err = client.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(context.Background(), mutatingWebhookConfiguration("mutating-webhook"), metav1.CreateOptions{})
	require.NoError(t, err)

	// wait for the informer to be aware of this create
	task = func() bool {
		return len(observedMutatingWebhooks) == 1
	}
	await(t, task, "MutatingWebhookConfiguration should have 1 MutatingWebhookConfiguration")
	require.Equal(t, 1, len(observedMutatingWebhooks))
	require.Equal(t, "mutating-webhook", observedMutatingWebhooks[0].Name)
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

// await - sleep for a short period and invoke the given task. return if the task returns true. repeat for a fixed number of iterations
func await(t *testing.T, task func() bool, message string) {
	for i := 0; i < 10; i++ {
		time.Sleep(10 * time.Millisecond)
		if task() {
			return
		}
	}
	require.Fail(t, message)
}
