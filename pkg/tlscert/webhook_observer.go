package tlscert

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/pkg/errors"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync
	informerSyncInterval = 5 * time.Minute
)

type WebhookConfigurationListener struct {
	OnValidatingWebhookConfiguration func(webhook *admissionregistrationv1.ValidatingWebhookConfiguration) error
	OnMutatingWebhookConfiguration   func(webhook *admissionregistrationv1.MutatingWebhookConfiguration) error
}

// A WebhookObserver uses informers to monitor for ValidatingWebhookConfiguration and MutatingWebhookConfigurations.
// When observed, a call back function is invoked with the given configuration.
// The call back function is set as part of the Init() function.
type WebhookObserver struct {
	factory                    informers.SharedInformerFactory
	informerValidatingWebhooks cache.SharedIndexInformer
	informerMutatingWebhooks   cache.SharedIndexInformer
	log                        log.Logger
	stopCh                     chan struct{}
	lock                       sync.Mutex
	onEvent                    *WebhookConfigurationListener
}

func NewWebhookObserver(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *WebhookObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	factory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)

	c := &WebhookObserver{
		factory:                    factory,
		informerValidatingWebhooks: factory.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
		informerMutatingWebhooks:   factory.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
		log:                        logger,
		stopCh:                     make(chan struct{}),
		lock:                       sync.Mutex{},
	}

	return c
}

func (c *WebhookObserver) onWebHookConfigurationObserved(obj interface{}) {
	// avoid any concurrent modifications where the same webhook is passed in
	c.lock.Lock()
	defer c.lock.Unlock()

	var err error
	switch obj := obj.(type) {
	case *admissionregistrationv1.ValidatingWebhookConfiguration:
		err = c.onEvent.OnValidatingWebhookConfiguration(obj)
	case *admissionregistrationv1.MutatingWebhookConfiguration:
		err = c.onEvent.OnMutatingWebhookConfiguration(obj)
	default:
		level.Error(c.log).Log("msg", "unknown object", "type", fmt.Sprintf("%T", obj))
	}
	if err != nil {
		level.Error(c.log).Log("msg", "unknown to call configuration listener", "err", err)
	}
}

// Init starts watching for validating and mutating webhook configurations being added.
// The given WebhookConfigurationListener is called when any webhook configurations is added.
func (c *WebhookObserver) Init(onEvent *WebhookConfigurationListener) error {
	c.onEvent = onEvent

	informers := []cache.SharedIndexInformer{c.informerValidatingWebhooks, c.informerMutatingWebhooks}
	for _, informer := range informers {
		if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.onWebHookConfigurationObserved(obj)
			},
			UpdateFunc: func(_, new interface{}) {
				c.onWebHookConfigurationObserved(new)
			},
		}); err != nil {
			return errors.Wrap(err, "failed to add webhook listener")
		}
	}

	go c.factory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.log).Log("msg", "waiting for webhook informer caches to sync")
	if ok := cache.WaitForCacheSync(c.stopCh, c.informerValidatingWebhooks.HasSynced, c.informerMutatingWebhooks.HasSynced); !ok {
		return fmt.Errorf("validating webhook informer caches failed to sync. validating webhook informer sync=%t, mutating webhook informer sync=%t", c.informerValidatingWebhooks.HasSynced(), c.informerMutatingWebhooks.HasSynced())
	}
	level.Info(c.log).Log("msg", "webhook informer caches sync completed")

	return nil
}

func (c *WebhookObserver) Stop() {
	close(c.stopCh)
}
