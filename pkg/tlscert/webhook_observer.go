package tlscert

import (
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
	namespace                  string
	factory                    informers.SharedInformerFactory
	informerValidatingWebhooks cache.SharedIndexInformer
	informerMutatingWebhooks   cache.SharedIndexInformer
	log                        log.Logger
	stopCh                     chan struct{}
}

func NewWebhookObserver(kubeClient kubernetes.Interface, namespace string, logger log.Logger) *WebhookObserver {
	namespaceOpt := informers.WithNamespace(namespace)

	factory := informers.NewSharedInformerFactoryWithOptions(kubeClient, informerSyncInterval, namespaceOpt)

	c := &WebhookObserver{
		namespace:                  namespace,
		factory:                    factory,
		informerValidatingWebhooks: factory.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
		informerMutatingWebhooks:   factory.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
		log:                        logger,
		stopCh:                     make(chan struct{}),
	}

	return c
}

// Init starts watching for validating and mutating webhook configurations being added.
// The given WebhookConfigurationListener is called when any webhook configurations is added.
func (c *WebhookObserver) Init(onEvent *WebhookConfigurationListener) error {

	informers := []cache.SharedIndexInformer{c.informerValidatingWebhooks, c.informerMutatingWebhooks}
	for _, informer := range informers {
		if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				var err error
				validatingWebHook, isObj := obj.(*admissionregistrationv1.ValidatingWebhookConfiguration)
				if isObj {
					err = onEvent.OnValidatingWebhookConfiguration(validatingWebHook)
				} else {
					mutatingWebHook, isObj := obj.(*admissionregistrationv1.MutatingWebhookConfiguration)
					if isObj {
						err = onEvent.OnMutatingWebhookConfiguration(mutatingWebHook)
					}
				}
				if err != nil {
					level.Error(c.log).Log("msg", "Unable to call webhook configuration listener", "err", err)
				}
			},
		}); err != nil {
			return errors.Wrap(err, "failed to add webhook listener")
		}
	}

	go c.factory.Start(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.log).Log("msg", "waiting for webhook informer caches to sync")
	if ok := cache.WaitForCacheSync(c.stopCh, c.informerValidatingWebhooks.HasSynced, c.informerMutatingWebhooks.HasSynced); !ok {
		return errors.New("validating webhook informer caches failed to sync")
	}
	level.Info(c.log).Log("msg", "webhook informer caches sync completed")

	return nil
}

func (c *WebhookObserver) Stop() {
	close(c.stopCh)
}
