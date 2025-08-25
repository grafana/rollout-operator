package tlscert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	v1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// PatchCABundleOnValidatingWebhook patches the CA bundle of all validating webhook configurations that have the specified labels in the cluster.
// Webhook configurations should have the following labels:
// "grafana.com/inject-rollout-operator-ca": "true",
// "grafana.com/namespace":                  <specified namespace>,
func PatchCABundleOnValidatingWebhook(logger log.Logger, kubeClient kubernetes.Interface, namespace string, caPEM []byte, wh *v1.ValidatingWebhookConfiguration) error {
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		"grafana.com/inject-rollout-operator-ca": "true",
		"grafana.com/namespace":                  namespace,
	}}

	if selector, err := metav1.LabelSelectorAsSelector(&labelSelector); err != nil {
		return err
	} else if !selector.Matches(labels.Set(wh.GetLabels())) {
		return nil
	}

	changed := false
	for i := range wh.Webhooks {
		if !bytes.Equal(wh.Webhooks[i].ClientConfig.CABundle, caPEM) {
			wh.Webhooks[i].ClientConfig.CABundle = caPEM
			changed = true
		}
	}

	if !changed {
		level.Info(logger).Log("msg", "validating webhook configuration already has same CA bundle set, not patching", "name", wh.GetName())
		return nil
	}

	data, err := json.Marshal(wh)
	if err != nil {
		return fmt.Errorf("failed to marshal validating webhook configuration: %w", err)
	}

	res, err := kubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().Patch(context.Background(), wh.GetName(), types.MergePatchType, data, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch validating webhook configuration: %w", err)
	}
	level.Info(logger).Log("msg", "patched validating webhook configuration with CA bundle", "name", res.GetName())
	return nil
}

// PatchCABundleOnMutatingWebhook patches the CA bundle of all mutating webhook configurations that have the specified labels in the cluster.
// Webhook configurations should have the following labels:
// "grafana.com/inject-rollout-operator-ca": "true",
// "grafana.com/namespace":                  <specified namespace>,
func PatchCABundleOnMutatingWebhook(logger log.Logger, kubeClient kubernetes.Interface, namespace string, caPEM []byte, wh *v1.MutatingWebhookConfiguration) error {
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		"grafana.com/inject-rollout-operator-ca": "true",
		"grafana.com/namespace":                  namespace,
	}}

	if selector, err := metav1.LabelSelectorAsSelector(&labelSelector); err != nil {
		return err
	} else if !selector.Matches(labels.Set(wh.GetLabels())) {
		return nil
	}

	changed := false
	for i := range wh.Webhooks {
		if !bytes.Equal(wh.Webhooks[i].ClientConfig.CABundle, caPEM) {
			wh.Webhooks[i].ClientConfig.CABundle = caPEM
			changed = true
		}
	}
	if !changed {
		level.Info(logger).Log("msg", "mutating webhook configuration already has same CA bundle set, not patching", "name", wh.GetName())
		return nil
	}

	data, err := json.Marshal(wh)
	if err != nil {
		return fmt.Errorf("failed to marshal mutating webhook configuration: %w", err)
	}

	res, err := kubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Patch(context.Background(), wh.GetName(), types.MergePatchType, data, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch mutating webhook configuration: %w", err)
	}
	level.Info(logger).Log("msg", "patched mutating webhook configuration with CA bundle", "name", res.GetName())
	return nil
}
