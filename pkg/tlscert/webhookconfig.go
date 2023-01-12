package tlscert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// PatchCABundleOnValidatingWebhooks patches the CA bundle of all validating webhook configurations that have the specified labels in the cluster.
// Webhook configurations should have the following labels:
// "grafana.com/inject-rollout-operator-ca": "true",
// "grafana.com/namespace":                  <specified namespace>,
func PatchCABundleOnValidatingWebhooks(ctx context.Context, logger log.Logger, kubeClient kubernetes.Interface, namespace string, caPEM []byte) error {
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		"grafana.com/inject-rollout-operator-ca": "true",
		"grafana.com/namespace":                  namespace,
	}}

	whcs, err := kubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	})
	if err != nil {
		return fmt.Errorf("failed to list validating webhook configurations: %w", err)
	}
	if len(whcs.Items) == 0 {
		level.Info(logger).Log("msg", "no validating webhook configurations found")
		return nil
	}

	for _, wh := range whcs.Items {
		level.Info(logger).Log("msg", "found validating webhook configuration", "name", wh.GetName())

		changed := false
		for i := range wh.Webhooks {
			if !bytes.Equal(wh.Webhooks[i].ClientConfig.CABundle, caPEM) {
				wh.Webhooks[i].ClientConfig.CABundle = caPEM
				changed = true
			}
		}
		if !changed {
			level.Info(logger).Log("msg", "validating webhook configuration already has same CA bundle set, not patching", "name", wh.GetName())
			continue
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
	}

	return nil
}
