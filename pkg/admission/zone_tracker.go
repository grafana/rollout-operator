package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/pkg/config"
	v1 "k8s.io/api/admission/v1"
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

// Use a ConfigMap instead of an annotation to track the last time zones were downscaled
func (zt *zoneTracker) prepareDownscale(ctx context.Context, logger log.Logger, ar v1.AdmissionReview, api kubernetes.Interface, client httpClient) *v1.AdmissionResponse {
	logger = log.With(logger, "name", ar.Request.Name, "resource", ar.Request.Resource.Resource, "namespace", ar.Request.Namespace)

	if *ar.Request.DryRun {
		return &v1.AdmissionResponse{Allowed: true}
	}

	oldInfo, err := decodeAndReplicas(ar.Request.OldObject.Raw)
	if err != nil {
		return allowErr(logger, "can't decode old object, allowing the change", err)
	}
	logger = log.With(logger, "request_gvk", oldInfo.gvk, "old_replicas", int32PtrStr(oldInfo.replicas))

	newInfo, err := decodeAndReplicas(ar.Request.Object.Raw)
	if err != nil {
		return allowErr(logger, "can't decode new object, allowing the change", err)
	}
	logger = log.With(logger, "new_replicas", int32PtrStr(newInfo.replicas))

	// Continue if it's a downscale
	response := checkReplicasChange(logger, oldInfo, newInfo)
	if response != nil {
		return response
	}

	// Get the labels and annotations from the old object including the prepare downscale label
	lbls, annotations, err := getLabelsAndAnnotations(ctx, ar, api, oldInfo)
	if err != nil {
		return allowWarn(logger, fmt.Sprintf("%s, allowing the change", err))
	}

	if lbls[config.PrepareDownscaleLabelKey] != config.PrepareDownscaleLabelValue {
		// Not labeled, nothing to do.
		return &v1.AdmissionResponse{Allowed: true}
	}

	port := annotations[config.PrepareDownscalePortAnnotationKey]
	if port == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePortAnnotationKey))
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePortAnnotationKey,
		)
	}

	path := annotations[config.PrepareDownscalePathAnnotationKey]
	if path == "" {
		level.Warn(logger).Log("msg", fmt.Sprintf("downscale not allowed because the %v annotation is not set or empty", config.PrepareDownscalePathAnnotationKey))
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because the %v annotation is not set or empty.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, config.PrepareDownscalePathAnnotationKey,
		)
	}

	rolloutGroup := lbls[config.RolloutGroupLabelKey]
	if rolloutGroup != "" {
		stsList, err := findStatefulSetsForRolloutGroup(ctx, api, ar.Request.Namespace, rolloutGroup)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while finding other statefulsets", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because finding other statefulsets failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			)
		}
		if err := zt.loadZones(ctx, stsList); err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while loading zones", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because loading zones failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			)
		}
		// Check if the zone has been downscaled recently.
		foundSts, err := zt.findDownscalesDoneMinTimeAgo(stsList, ar.Request.Name)
		if err != nil {
			level.Warn(logger).Log("msg", "downscale not allowed due to error while parsing downscale timestamps from the zone ConfigMap", "err", err)
			return deny(
				"downscale of %s/%s in %s from %d to %d replicas is not allowed because parsing parsing downscale timestamps from the zone ConfigMap failed.",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
			)
		}
		if foundSts != nil {
			msg := fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is not allowed because statefulset %v was downscaled at %v and is labelled to wait %s between zone downscales",
				ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas, foundSts.name, foundSts.lastDownscaleTime, foundSts.waitTime)
			level.Warn(logger).Log("msg", msg, "err", err)
			return deny(msg)
		}
	}

	// Since it's a downscale, check if the resource has the label that indicates it needs to be prepared to be downscaled.
	// Create a slice of endpoint addresses for pods to send HTTP post requests to and to fail if any don't return 200
	eps := createEndpoints(ar, oldInfo, newInfo, port, path)

	err = sendPrepareShutdownRequests(ctx, logger, client, eps)
	if err != nil {
		// Down-scale operation is disallowed because a pod failed to prepare for shutdown and cannot be deleted
		level.Error(logger).Log("msg", "downscale not allowed due to error", "err", err)
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because one or more pods failed to prepare for shutdown.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
		)
	}

	if err := zt.setDownscaled(ctx, ar.Request.Name); err != nil {
		level.Error(logger).Log("msg", "downscale not allowed due to error while setting downscale timestamp in the zone ConfigMap", "err", err)
		return deny(
			"downscale of %s/%s in %s from %d to %d replicas is not allowed because setting downscale timestamp in the zone ConfigMap failed.",
			ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas,
		)
	}

	// Down-scale operation is allowed because all pods successfully prepared for shutdown
	level.Info(logger).Log("msg", "downscale allowed")
	return &v1.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Message: fmt.Sprintf("downscale of %s/%s in %s from %d to %d replicas is allowed -- all pods successfully prepared for shutdown.", ar.Request.Resource.Resource, ar.Request.Name, ar.Request.Namespace, *oldInfo.replicas, *newInfo.replicas),
		},
	}
}

// Create the ConfigMap and populate each zone with the current time as a starting point
func (zt *zoneTracker) createConfigMap(ctx context.Context, stsList *appsv1.StatefulSetList) (*corev1.ConfigMap, error) {
	defaultInfo := &zoneInfo{LastDownscaled: time.Now().UTC().Format(time.RFC3339)}
	zones := make(map[string]zoneInfo, len(stsList.Items))
	for _, sts := range stsList.Items {
		if _, ok := zones[sts.Name]; !ok {
			zones[sts.Name] = *defaultInfo
		}
	}

	data := make(map[string]string, len(zones))
	for zone, zi := range zones {
		ziBytes, err := json.Marshal(zi)
		if err != nil {
			return nil, err
		}
		data[zone] = string(ziBytes)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      zt.configMapName,
			Namespace: zt.namespace,
		},
		Data: data,
	}

	return zt.client.CoreV1().ConfigMaps(zt.namespace).Create(ctx, cm, metav1.CreateOptions{})
}

// Get the zoneTracker ConfigMap if it exists, otherwise create it
func (zt *zoneTracker) getOrCreateConfigMap(ctx context.Context, stsList *appsv1.StatefulSetList) (*corev1.ConfigMap, error) {
	cm, err := zt.client.CoreV1().ConfigMaps(zt.namespace).Get(ctx, zt.configMapName, metav1.GetOptions{})
	if err == nil {
		return cm, nil
	}
	if !k8serrors.IsNotFound(err) {
		return nil, err
	}

	return zt.createConfigMap(ctx, stsList)
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

	info, ok := zt.zones[zone]
	if !ok {
		// If the zone is not found, create it and add it to the zones map
		info := &zoneInfo{LastDownscaled: time.Now().UTC().Format(time.RFC3339)}
		zt.zones[zone] = *info
	} else {
		// If the zone is found, update the LastDownscaled time
		info.LastDownscaled = time.Now().UTC().Format(time.RFC3339)
		// Update the zones map with the new zoneInfo
		zt.zones[zone] = info
	}

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

func newZoneTracker(api kubernetes.Interface, namespace string, configMapName string) *zoneTracker {
	return &zoneTracker{
		zones:         make(map[string]zoneInfo),
		client:        api,
		namespace:     namespace,
		configMapName: configMapName,
	}
}
