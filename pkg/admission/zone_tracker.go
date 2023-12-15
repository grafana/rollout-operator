package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type zoneTracker struct {
	mu            sync.Mutex
	zones         map[string]zoneInfo
	client        kubernetes.Interface
	namespace     string
	configMapName string
}

type zoneInfo struct {
	LastDownscaled string `json:"lastDownscaled"`
}

// Get the zoneTracker ConfigMap or create it and populate it with the current time for each zone if it doesn't exist
func (zt *zoneTracker) getOrCreateConfigMap(ctx context.Context, stsList *appsv1.StatefulSetList) (*corev1.ConfigMap, error) {
	cm, err := zt.client.CoreV1().ConfigMaps(zt.namespace).Get(ctx, zt.configMapName, metav1.GetOptions{})
	if err == nil {
		return cm, nil
	}
	if !errors.IsNotFound(err) {
		return nil, err
	}

	// If the ConfigMap does not exist, create it
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      zt.configMapName,
			Namespace: zt.namespace,
		},
		Data: make(map[string]string),
	}
	cm, err = zt.client.CoreV1().ConfigMaps(zt.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	// Populate initial zones after the configmap is created
	if err := zt.createInitialZones(ctx, stsList); err != nil {
		return nil, err
	}

	return cm, nil
}

// Load the zones from the zoneTracker ConfigMap into the zones map
func (zt *zoneTracker) loadZones(ctx context.Context, stsList *appsv1.StatefulSetList) error {
	cm, err := zt.getOrCreateConfigMap(ctx, stsList)
	if err != nil {
		return err
	}

	// Convert the ConfigMap data to the zones map
	for zone, data := range cm.Data {
		var zi zoneInfo
		err = json.Unmarshal([]byte(data), &zi)
		if err != nil {
			return err
		}
		zt.zones[zone] = zi
	}

	return nil
}

// Save the zones map to the zoneTracker ConfigMap
func (zt *zoneTracker) saveZones(ctx context.Context) error {
	// Convert the zones map to ConfigMap data
	data := make(map[string]string)
	for zone, zi := range zt.zones {
		ziBytes, err := json.Marshal(zi)
		if err != nil {
			return err
		}
		data[zone] = string(ziBytes)
	}

	// Update the ConfigMap with the new data
	_, err := zt.client.CoreV1().ConfigMaps(zt.namespace).Update(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      zt.configMapName,
			Namespace: zt.namespace,
		},
		Data: data,
	}, metav1.UpdateOptions{})

	return err
}

// lastDownscaled returns the last time the zone was downscaled in UTC in time.RFC3339 format.
func (zt *zoneTracker) lastDownscaled(zone string) (string, error) {
	zoneInfo, ok := zt.zones[zone]
	if !ok {
		return "", fmt.Errorf("zone %s not found", zone)
	}

	return zoneInfo.LastDownscaled, nil
}

// setDownscaled sets the last time the zone was downscaled to the current time in UTC in time.RFC3339 format.
func (zt *zoneTracker) setDownscaled(ctx context.Context, zone string) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	zoneInfo, ok := zt.zones[zone]
	if !ok {
		return fmt.Errorf("zone %s not found", zone)
	}

	zoneInfo.LastDownscaled = time.Now().UTC().Format(time.RFC3339)

	// Update the zones map with the new zoneInfo
	zt.zones[zone] = zoneInfo

	return zt.saveZones(ctx)
}

// findDownscalesDoneMinTimeAgo returns the statefulset that was downscaled the least amount of time ago
func (zt *zoneTracker) findDownscalesDoneMinTimeAgo(stsList *appsv1.StatefulSetList, stsName string) (*statefulSetDownscale, error) {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	for _, sts := range stsList.Items {
		if sts.Name == stsName {
			continue
		}

		lastDownscaleStr, err := zt.lastDownscaled(sts.Name)
		if err != nil {
			// No last downscale timestamp set for the statefulset, we can continue
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

// If the zone file does not exist, populate it with the current time for each zone and upload it to the bucket.
func (zt *zoneTracker) createInitialZones(ctx context.Context, stsList *appsv1.StatefulSetList) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	currentTime := time.Now().UTC().Format(time.RFC3339)
	zoneInfo := &zoneInfo{LastDownscaled: currentTime}

	for _, sts := range stsList.Items {
		if _, ok := zt.zones[sts.Name]; !ok {
			zt.zones[sts.Name] = *zoneInfo
		}
	}

	return zt.saveZones(ctx)
}

func newZoneTracker(api kubernetes.Interface, namespace string, configMapName string) *zoneTracker {
	return &zoneTracker{
		zones:         make(map[string]zoneInfo),
		client:        api,
		namespace:     namespace,
		configMapName: configMapName,
	}
}
