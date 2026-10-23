package zpdb

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	k8cache "k8s.io/client-go/tools/cache"
)

const (
	// How frequently informers should resync
	informerSyncInterval   = 5 * time.Minute
	readinessCheckTimeout  = 10 * time.Second
	readinessCheckInterval = 30 * time.Second
)

// An configObserver facilitates listening for ZoneAwarePodDisruptionBudget changes, parsing and storing these into the configCache.
type configObserver struct {
	ctx              context.Context
	cancel           context.CancelFunc
	pdbInformer      k8cache.SharedIndexInformer
	pdbHandler       k8cache.ResourceEventHandlerRegistration
	watchHealthy     atomic.Bool
	readinessChanged chan struct{}
	pdbCache         *configCache
	metrics          *Metrics

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
	ctx, cancel := context.WithCancel(context.Background())
	c := &configObserver{
		ctx:              ctx,
		cancel:           cancel,
		pdbCache:         newConfigCache(),
		dynamicClient:    dynamic,
		pdbResource:      dynamic.Resource(gvr).Namespace(namespace),
		metrics:          metrics,
		logger:           logger,
		stopCh:           make(chan struct{}),
		readinessChanged: make(chan struct{}, 1),
	}

	c.pdbInformer = k8cache.NewSharedIndexInformer(k8cache.ToListWatcherWithWatchListSemantics(&k8cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			return c.pdbResource.List(ctx, opts)
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			w, err := c.pdbResource.Watch(ctx, opts)
			c.setWatchHealthy(err == nil)
			return w, err
		},
	}, dynamic), &unstructured.Unstructured{}, informerSyncInterval, k8cache.Indexers{k8cache.NamespaceIndex: k8cache.MetaNamespaceIndexFunc})

	return c
}

func (c *configObserver) start() error {
	handler, err := c.pdbInformer.AddEventHandler(k8cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onPdbAdded,
		UpdateFunc: c.onPdbUpdated,
		DeleteFunc: c.onPdbDeleted,
	})
	if err != nil {
		return err
	}
	c.pdbHandler = handler
	if err := c.pdbInformer.SetWatchErrorHandler(func(_ *k8cache.Reflector, err error) {
		c.setWatchHealthy(false)
		level.Warn(c.logger).Log("msg", "zpdb config observer watch failed", "err", err)
	}); err != nil {
		return err
	}

	c.metrics.ConfigObserverReady.WithLabelValues().Set(0)
	go c.pdbInformer.Run(c.stopCh)

	// Wait until all informer caches have been synced.
	level.Info(c.logger).Log("msg", "zpdb config informer caches are syncing")
	if ok := k8cache.WaitForCacheSync(c.stopCh, c.pdbHandler.HasSynced); !ok {
		return errors.New("zpdb config informer caches failed to sync")
	}
	level.Info(c.logger).Log("msg", "zpdb config informer caches have synced")
	go c.observeReadiness()

	return nil
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
		case <-c.readinessChanged:
			c.updateReadiness()
		}
	}
}

func (c *configObserver) setWatchHealthy(healthy bool) {
	if c.watchHealthy.Swap(healthy) != healthy {
		// Recheck promptly after a watch opens or fails instead of waiting for the periodic probe.
		select {
		case c.readinessChanged <- struct{}{}:
		default:
		}
	}
}

func (c *configObserver) updateReadiness() {
	ctx, cancel := context.WithTimeout(c.ctx, readinessCheckTimeout)
	defer cancel()
	_, err := c.pdbResource.List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil && c.pdbHandler.HasSynced() && c.watchHealthy.Load() {
		c.metrics.ConfigObserverReady.WithLabelValues().Set(1)
	} else {
		c.metrics.ConfigObserverReady.WithLabelValues().Set(0)
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
	c.cancel()
	close(c.stopCh)
}
