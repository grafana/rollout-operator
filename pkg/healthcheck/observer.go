package healthcheck

import (
	"errors"
	"reflect"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	k8cache "k8s.io/client-go/tools/cache"
)

const informerSyncInterval = 5 * time.Minute

// Observer watches RolloutHealthCheck objects and keeps an in-memory cache.
type Observer struct {
	factory  dynamicinformer.DynamicSharedInformerFactory
	informer k8cache.SharedIndexInformer
	cache    *configCache
	metrics  *Metrics
	logger   log.Logger
	stopCh   chan struct{}
}

// NewObserver creates a namespace-scoped observer for RolloutHealthCheck resources.
func NewObserver(dyn dynamic.Interface, namespace string, logger log.Logger, metrics *Metrics) *Observer {
	gvr := schema.GroupVersionResource{
		Group:    RolloutHealthChecksSpecGroup,
		Version:  RolloutHealthChecksVersion,
		Resource: RolloutHealthChecksPlural,
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, informerSyncInterval, namespace, nil)
	informer := factory.ForResource(gvr)

	return &Observer{
		factory:  factory,
		informer: informer.Informer(),
		cache:    newConfigCache(),
		metrics:  metrics,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// Start begins watching and blocks until the informer cache has synced.
func (o *Observer) Start() error {
	_, err := o.informer.AddEventHandler(k8cache.ResourceEventHandlerFuncs{
		AddFunc:    o.onAdded,
		UpdateFunc: o.onUpdated,
		DeleteFunc: o.onDeleted,
	})
	if err != nil {
		return err
	}

	go o.factory.Start(o.stopCh)

	level.Info(o.logger).Log("msg", "rollout health check informer caches are syncing")
	if ok := k8cache.WaitForCacheSync(o.stopCh, o.informer.HasSynced); !ok {
		return errors.New("rollout health check informer caches failed to sync")
	}
	level.Info(o.logger).Log("msg", "rollout health check informer caches have synced")
	return nil
}

// Stop stops the informer.
func (o *Observer) Stop() {
	close(o.stopCh)
}

// Get returns the cached config for name, or nil if missing / invalid.
func (o *Observer) Get(name string) *Config {
	return o.cache.Get(name)
}

func (o *Observer) addOrUpdate(obj *unstructured.Unstructured) {
	updated, generation, err := o.cache.addOrUpdateRaw(obj)
	if err != nil {
		level.Error(o.logger).Log("msg", "rollout health check configuration error", "name", obj.GetName(), "err", err)
		// Drop any previously cached valid config so we do not keep evaluating stale rules.
		o.cache.invalidate(obj.GetName())
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("invalid").Inc()
		}
		return
	}
	if updated {
		level.Info(o.logger).Log("msg", "rollout health check configuration updated", "name", obj.GetName(), "generation", generation)
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("updated").Inc()
		}
		return
	}
	level.Info(o.logger).Log("msg", "rollout health check configuration update ignored", "name", obj.GetName(), "generation", generation, "ignored-generation", obj.GetGeneration())
	if o.metrics != nil {
		o.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
	}
}

func (o *Observer) onAdded(obj interface{}) {
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		}
		level.Warn(o.logger).Log("msg", "unexpected object passed through health check informer", "type", reflect.TypeOf(obj))
		return
	}
	o.addOrUpdate(unstructuredObj)
}

func (o *Observer) onUpdated(old, new interface{}) {
	_, oldOK := old.(*unstructured.Unstructured)
	newCfg, newOK := new.(*unstructured.Unstructured)
	if !oldOK || !newOK {
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		}
		level.Warn(o.logger).Log("msg", "unexpected object passed through health check informer", "old_type", reflect.TypeOf(old), "new_type", reflect.TypeOf(new))
		return
	}
	o.addOrUpdate(newCfg)
}

func (o *Observer) onDeleted(obj interface{}) {
	if tombstone, ok := obj.(k8cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	oldCfg, ok := obj.(*unstructured.Unstructured)
	if !ok {
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("ignored").Inc()
		}
		level.Warn(o.logger).Log("msg", "unexpected object passed through health check informer", "type", reflect.TypeOf(obj))
		return
	}
	success, generation := o.cache.delete(oldCfg.GetGeneration(), oldCfg.GetName())
	if success {
		if o.metrics != nil {
			o.metrics.ConfigurationsObserved.WithLabelValues("deleted").Inc()
		}
		level.Info(o.logger).Log("msg", "rollout health check configuration deleted", "name", oldCfg.GetName(), "generation", generation)
		return
	}
	if o.metrics != nil {
		o.metrics.ConfigurationsObserved.WithLabelValues("delete-ignored").Inc()
	}
	level.Info(o.logger).Log("msg", "rollout health check configuration delete ignored", "name", oldCfg.GetName(), "generation", generation, "ignored-generation", oldCfg.GetGeneration())
}
