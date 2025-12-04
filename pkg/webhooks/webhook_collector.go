package webhooks

import (
	"sync"

	"github.com/grafana/rollout-operator/pkg/tlscert"
	"github.com/prometheus/client_golang/prometheus"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"

	"github.com/go-kit/log"
	"k8s.io/client-go/kubernetes"
)

const (
	name = "kube_validating_webhook_failure_policy"
	desc = "FailurePolicy setting of Kubernetes ValidatingWebhooks and MutatingWebhooks"
)

type WebhookCollector struct {
	lock                       sync.RWMutex
	observer                   *tlscert.WebhookObserver
	validatingWebhooksSettings map[string]admissionregistrationv1.FailurePolicyType
	mutatingWebhooksSettings   map[string]admissionregistrationv1.FailurePolicyType
	metricDescription          *prometheus.Desc
}

func NewWebhookCollector(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *WebhookCollector {
	return &WebhookCollector{
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
		ch <- prometheus.MustNewConstMetric(c.metricDescription, prometheus.GaugeValue, 1, "ValidatingWebhook", name, string(failureMode))
	}
	for name, failureMode := range c.mutatingWebhooksSettings {
		ch <- prometheus.MustNewConstMetric(c.metricDescription, prometheus.GaugeValue, 1, "MutatingWebhook", name, string(failureMode))
	}
}
