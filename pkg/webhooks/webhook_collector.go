package webhooks

import (
	"sync"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/tlscert"
)

const (
	name = "kube_validating_webhook_failure_policy"
	desc = "FailurePolicy setting of Kubernetes ValidatingWebhooks and MutatingWebhooks"
)

var policyTypes = []admissionregistrationv1.FailurePolicyType{admissionregistrationv1.Fail, admissionregistrationv1.Ignore}

type WebhookCollector struct {
	lock                       sync.RWMutex
	labelSelector              *metav1.LabelSelector
	observer                   *tlscert.WebhookObserver
	validatingWebhooksSettings map[string]admissionregistrationv1.FailurePolicyType
	mutatingWebhooksSettings   map[string]admissionregistrationv1.FailurePolicyType
	metricDescription          *prometheus.Desc
}

func NewWebhookCollector(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *WebhookCollector {

	return &WebhookCollector{
		labelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
			"grafana.com/namespace": namespace,
		}},
		observer:                   tlscert.NewWebhookObserver(kubeClient, namespace, logger),
		validatingWebhooksSettings: make(map[string]admissionregistrationv1.FailurePolicyType),
		mutatingWebhooksSettings:   make(map[string]admissionregistrationv1.FailurePolicyType),
		metricDescription: prometheus.NewDesc(
			name,
			desc,
			[]string{"type", "webhook", "policy"}, nil,
		),
	}
}

func (c *WebhookCollector) Start() error {

	// monitor for validating and mutating webhooks and maintain a local cache of their failure policy settings
	webHookListener := &tlscert.WebhookConfigurationListener{
		OnValidatingWebhookConfiguration: func(webhook *admissionregistrationv1.ValidatingWebhookConfiguration) error {
			if selector, err := metav1.LabelSelectorAsSelector(c.labelSelector); err != nil {
				return err
			} else if !selector.Matches(labels.Set(webhook.GetLabels())) {
				return nil
			}

			c.lock.Lock()
			defer c.lock.Unlock()
			for _, wh := range webhook.Webhooks {
				if wh.FailurePolicy == nil {
					// default kubernetes behaviour is to fail
					c.validatingWebhooksSettings[wh.Name] = admissionregistrationv1.Fail
				} else {
					c.validatingWebhooksSettings[wh.Name] = *wh.FailurePolicy
				}
			}
			return nil
		},
		OnMutatingWebhookConfiguration: func(webhook *admissionregistrationv1.MutatingWebhookConfiguration) error {
			if selector, err := metav1.LabelSelectorAsSelector(c.labelSelector); err != nil {
				return err
			} else if !selector.Matches(labels.Set(webhook.GetLabels())) {
				return nil
			}

			c.lock.Lock()
			defer c.lock.Unlock()
			for _, wh := range webhook.Webhooks {
				if wh.FailurePolicy == nil {
					// default kubernetes behaviour is to fail
					c.mutatingWebhooksSettings[wh.Name] = admissionregistrationv1.Fail
				} else {
					c.mutatingWebhooksSettings[wh.Name] = *wh.FailurePolicy
				}
			}
			return nil
		},
	}

	if err := c.observer.Init(webHookListener); err != nil {
		return err
	}

	return nil
}

func (c *WebhookCollector) Stop() {
	c.observer.Stop()
}

func (c *WebhookCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.metricDescription
}

func (c *WebhookCollector) Collect(ch chan<- prometheus.Metric) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	for name, failureMode := range c.validatingWebhooksSettings {
		for _, policyType := range policyTypes {
			ch <- prometheus.MustNewConstMetric(c.metricDescription, prometheus.GaugeValue, c.boolToFloat(failureMode == policyType), "ValidatingWebhook", name, string(policyType))
		}
	}
	for name, failureMode := range c.mutatingWebhooksSettings {
		for _, policyType := range policyTypes {
			ch <- prometheus.MustNewConstMetric(c.metricDescription, prometheus.GaugeValue, c.boolToFloat(failureMode == policyType), "MutatingWebhook", name, string(policyType))
		}
	}
}

func (c *WebhookCollector) boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
