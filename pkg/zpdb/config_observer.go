package zpdb

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	k8cache "k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync
	informerSyncInterval        = 5 * time.Minute
	initialResourceCheckTimeout = 10 * time.Second
	readinessCheckInterval      = 30 * time.Second
)

// An configObserver facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the configCache.
type configObserver struct {
	pdbFactory  dynamicinformer.DynamicSharedInformerFactory
	pdbInformer k8cache.SharedIndexInformer
	pdbCache    *configCache
	metrics     *Metrics

	dynamicClient dynamic.Interface
	pdbResource   dynamic.ResourceInterface
	logger        log.Logger

	// Used to signal when the controller should stop.
	stopCh chan struct{}
}

func newConfigObserver(dynamic dynamic.Interface, namespace string, logger log.Logger, metrics *Metrics) *configObserver {
	gvr := schema.GroupVersionResource{
		Group:    ZoneAwarePodDisruptionBudgetsSpecGroup,
		Version:  ZoneAwarePodDisruptionBudgetsVersion,
		Resource: ZoneAwarePodDisruptionBudgetsNamePlural,
	}
	pdbFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dynamic,
		informerSyncInterval,
		namespace,
		nil,
	)
	pdbInformer := pdbFactory.ForResource(gvr)

	c := &configObserver{
		pdbFactory:    pdbFactory,
		pdbInformer:   pdbInformer.Informer(),
		pdbCache:      newConfigCache(),
		dynamicClient: dynamic,
		pdbResource:   dynamic.Resource(gvr).Namespace(namespace),
		metrics:       metrics,
		logger:        logger,
		stopCh:        make(chan struct{}),
	}

	return c
}

func (c *configObserver) start() error {
	_, err := c.pdbInformer.AddEventHandler(k8cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPdbAdded,
		UpdateFunc: c.onPdbUpdated,
		DeleteFunc: c.onPdbDeleted,
	})
	if err != nil {
		return err
	}
	if err := c.pdbInformer.SetWatchErrorHandler(func(_ *k8cache.Reflector, err error) {
		c.metrics.ConfigObserverReady.Set(0)
		level.Warn(c.logger).Log("msg", "zpdb config observer unavailable", "err", err)
	}); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), initialResourceCheckTimeout)
	_, listErr := c.pdbResource.List(ctx, metav1.ListOptions{Limit: 1})
	cancel()
	if listErr != nil && !apierrors.IsNotFound(listErr) {
		return listErr
	}

	go c.pdbFactory.Start(c.stopCh)
	go c.observeReadiness()

	if apierrors.IsNotFound(listErr) {
		level.Warn(c.logger).Log("msg", "zpdb custom resource is unavailable; informer will retry", "err", listErr)
		go c.waitForCacheSync()
		return nil
	}

	if !c.waitForCacheSync() {
		return errors.New("zpdb config informer caches failed to sync")
	}

	return nil
}

func (c *configObserver) waitForCacheSync() bool {
	level.Info(c.logger).Log("msg", "zpdb config informer caches are syncing")
	if ok := k8cache.WaitForCacheSync(c.stopCh, c.pdbInformer.HasSynced); !ok {
		return false
	}
	c.metrics.ConfigObserverReady.Set(1)
	level.Info(c.logger).Log("msg", "zpdb config informer caches have synced")
	return true
}

func (c *configObserver) observeReadiness() {
	c.updateReadiness()
	ticker := time.NewTicker(readinessCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.updateReadiness()
		}
	}
}

func (c *configObserver) updateReadiness() {
	_, err := c.pdbResource.List(context.Background(), metav1.ListOptions{Limit: 1})
	if err == nil && c.pdbInformer.HasSynced() {
		c.metrics.ConfigObserverReady.Set(1)
	} else {
		c.metrics.ConfigObserverReady.Set(0)
	}
}

func (c *configObserver) addOrUpdate(obj *unstructured.Unstructured) {
	updated, generation, err := c.pdbCache.addOrUpdateRaw(obj)
	if err != nil {
		level.Error(c.logger).Log("msg", "zpdb configuration error", "name", obj.GetName(), "err", err)
		c.metrics.ConfigurationsObserved.WithLabelValues("invalid").Inc()
	} else if updated {
		level.Info(c.logger).Log("msg", "zpdb configuration updated", "name", obj.GetName(), "generation", generation)
		c.metrics.ConfigurationsObserved.WithLabelValues("updated").Inc()
	} else {
		level.Info(c.logger).Log("msg", "zpdb configuration update ignored", "name", obj.GetName(), "generation", generation, "ignored-generation", obj.GetGeneration())
		c.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
	}
}

func (c *configObserver) onPdbAdded(obj interface{}) {
	unstructuredObj, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		c.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}

	c.addOrUpdate(unstructuredObj)
}

func (c *configObserver) onPdbUpdated(old, new interface{}) {
	_, oldIsUnstructured := old.(*unstructured.Unstructured)
	newCfg, newIsUnstructured := new.(*unstructured.Unstructured)

	if !oldIsUnstructured || !newIsUnstructured {
		c.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "old_type", reflect.TypeOf(old), "new_type", reflect.TypeOf(new))
		return
	}

	c.addOrUpdate(newCfg)
}

func (c *configObserver) onPdbDeleted(obj interface{}) {
	oldCfg, oldIsUnstructured := obj.(*unstructured.Unstructured)
	if !oldIsUnstructured {
		c.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		level.Warn(c.logger).Log("msg", "unexpected object passed through informer", "type", reflect.TypeOf(obj))
		return
	}
	success, generation := c.pdbCache.delete(oldCfg.GetGeneration(), oldCfg.GetName())
	if success {
		c.metrics.ConfigurationsObserved.WithLabelValues("deleted").Inc()
		level.Info(c.logger).Log("msg", "zpdb configuration deleted", "old", oldCfg.GetName(), "generation", generation)
	} else {
		c.metrics.ConfigurationsObserved.WithLabelValues("delete-ignored").Inc()
		level.Info(c.logger).Log("msg", "zpdb configuration delete ignored", "name", oldCfg.GetName(), "generation", generation, "ignored-generation", oldCfg.GetGeneration())
	}
}

func (c *configObserver) stop() {
	close(c.stopCh)
}
