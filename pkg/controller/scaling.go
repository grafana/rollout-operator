package controller

import (
	"context"

	"github.com/go-kit/log/level"
	"k8s.io/client-go/tools/cache"
)

func (c *RolloutController) enqueueIngesterAutoScaler(obj interface{}) {
	if key, err := cache.MetaNamespaceKeyFunc(obj); err != nil {
		level.Warn(c.logger).Log("msg", "getting MultiZoneIngesterAutoScaler from cache failed", "err", err)
		return
	} else {
		c.autoScalingQueue.Add(key)
	}
}

// ingesterAutoScalingWorker processes jobs from c.autoScalingQueue.
func (c *RolloutController) ingesterAutoScalingWorker(ctx context.Context) error {
	level.Info(c.logger).Log("msg", "reconciling ingester scaling custom resources...")

	return nil
}
