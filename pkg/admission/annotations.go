package admission

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	LastDownscaleAnnotationKey           = "grafana.com/last-downscale" // Should be in time.RFC3339 format
	MinTimeBetweenZonesDownscaleLabelKey = "grafana.com/min-time-between-zones-downscale"
)

func addDownscaledAnnotationToStatefulSet(ctx context.Context, api kubernetes.Interface, namespace, stsName string) error {
	client := api.AppsV1().StatefulSets(namespace)
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%v":"%v"}}}`, LastDownscaleAnnotationKey, time.Now().UTC().Format(time.RFC3339))
	_, err := client.Patch(ctx, stsName, types.StrategicMergePatchType, []byte(patch), v1.PatchOptions{})
	return err
}

type statefulSet struct {
	name              string
	waitTime          time.Duration
	lastDownscaleTime time.Time
}

func findDownscalesDoneMinTimeAgo(ctx context.Context, api kubernetes.Interface, namespace, stsName, rolloutGroup string) (*statefulSet, error) {
	client := api.AppsV1().StatefulSets(namespace)
	groupReq, err := labels.NewRequirement(RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
	if err != nil {
		return nil, err
	}
	sel := labels.NewSelector().Add(*groupReq)
	list, err := client.List(ctx, v1.ListOptions{
		LabelSelector: sel.String(),
	})
	if err != nil {
		return nil, err
	}

	for _, sts := range list.Items {
		if sts.Name == stsName {
			continue
		}
		lastDownscaleAnnotation, ok := sts.Annotations[LastDownscaleAnnotationKey]
		if !ok {
			// No last downscale label set on the statefulset, we can continue
			continue
		}

		downscaleTime, err := time.Parse(time.RFC3339, lastDownscaleAnnotation)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v annotation of %s: %w", LastDownscaleAnnotationKey, sts.Name, err)
		}

		timeBetweenDownscaleLabel, ok := sts.Labels[MinTimeBetweenZonesDownscaleLabelKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			continue
		}

		timeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v label of %s: %w", MinTimeBetweenZonesDownscaleLabelKey, sts.Name, err)
		}

		if downscaleTime.Add(timeBetweenDownscale).After(time.Now()) {
			s := statefulSet{
				name:              sts.Name,
				waitTime:          timeBetweenDownscale,
				lastDownscaleTime: downscaleTime,
			}
			return &s, nil
		}

	}
	return nil, nil
}
