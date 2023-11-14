package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/thanos-io/objstore"
	appsv1 "k8s.io/api/apps/v1"
)

type zoneTracker struct {
	mu    sync.Mutex
	zones map[string]string
	bkt   objstore.Bucket
	key   string
}

func (zt *zoneTracker) loadZones(ctx context.Context) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	r, err := zt.bkt.Get(ctx, zt.key)
	if err != nil {
		return err
	}
	defer r.Close()

	return json.NewDecoder(r).Decode(&zt.zones)
}

func (zt *zoneTracker) saveZones(ctx context.Context) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(zt.zones); err != nil {
		return err
	}

	return zt.bkt.Upload(ctx, zt.key, buf)
}

func (zt *zoneTracker) lastDownscaled(ctx context.Context, zone string) (string, error) {
	if err := zt.loadZones(ctx); err != nil {
		return "", err
	}

	zt.mu.Lock()
	defer zt.mu.Unlock()

	return zt.zones[zone], nil
}

func (zt *zoneTracker) setDownscaled(ctx context.Context, zone string) error {
	zt.mu.Lock()
	zt.zones[zone] = time.Now().UTC().Format(time.RFC3339)
	zt.mu.Unlock()

	return zt.saveZones(ctx)
}

func (zt *zoneTracker) findDownscalesDoneMinTimeAgo(stsList *appsv1.StatefulSetList, stsName string) (*statefulSetDownscale, error) {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	for _, sts := range stsList.Items {
		if sts.Name == stsName {
			continue
		}
		lastDownscaleStr, ok := zt.zones[sts.Name]
		if !ok {
			// No last downscale label set on the statefulset, we can continue
			continue
		}

		lastDownscale, err := time.Parse(time.RFC3339, lastDownscaleStr)
		if err != nil {
			return nil, fmt.Errorf("can't parse last downscale time of %s: %w", sts.Name, err)
		}

		timeBetweenDownscaleLabel, ok := sts.Labels[config.MinTimeBetweenZonesDownscaleLabelKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			continue
		}

		minTimeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v label of %s: %w", config.MinTimeBetweenZonesDownscaleLabelKey, sts.Name, err)
		}

		if time.Since(lastDownscale) < minTimeBetweenDownscale {
			s := statefulSetDownscale{
				name:              sts.Name,
				waitTime:          minTimeBetweenDownscale,
				lastDownscaleTime: lastDownscale,
			}
			return &s, nil
		}

	}
	return nil, nil
}

func (zt *zoneTracker) createInitialZones(ctx context.Context, stsList *appsv1.StatefulSetList) error {
	zt.mu.Lock()
	for _, sts := range stsList.Items {
		if _, ok := zt.zones[sts.Name]; !ok {
			zt.zones[sts.Name] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	zt.mu.Unlock()

	return zt.saveZones(ctx)
}

func newZoneTracker(bkt objstore.Bucket, key string) *zoneTracker {
	return &zoneTracker{
		zones: make(map[string]string),
		bkt:   bkt,
		key:   key,
	}
}
