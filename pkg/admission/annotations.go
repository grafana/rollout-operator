package admission

import (
	"context"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
)

const (
	LastDownscaleAnnotationKey             = "grafana.com/last-prepared-for-downscale" // Should be in time.RFC3339 format
	TimeBetweenZonesDownscaleAnnotationKey = "grafana.com/min-time-between-zones-downscale"
)

func addDownscaledAnnotationToStatefulSet(ctx context.Context, api kubernetes.Interface, namespace, stsName string) error {
	client := api.AppsV1().StatefulSets(namespace)
	sts, err := client.Get(ctx, stsName, v1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := sts.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[LastDownscaleAnnotationKey] = time.Now().UTC().Format(time.RFC3339)
	sts.SetAnnotations(annotations)

	_, err = client.Update(ctx, sts, v1.UpdateOptions{})
	return err
}

func findDownscalesDoneMinTimeAgo(ctx context.Context, api kubernetes.Interface, namespace, stsName, rolloutGroup string) (bool, error) {
	client := api.AppsV1().StatefulSets(namespace)
	groupReq, err := labels.NewRequirement(RolloutGroupLabelKey, selection.Equals, []string{rolloutGroup})
	if err != nil {
		return false, err
	}
	sel := labels.NewSelector().Add(*groupReq)
	list, err := client.List(ctx, v1.ListOptions{
		LabelSelector: sel.String(),
	})
	if err != nil {
		return false, err
	}

	for _, sts := range list.Items {
		if sts.Name == stsName {
			continue
		}
		lastDownscaleLabel, ok := sts.Labels[LastDownscaleAnnotationKey]
		if !ok {
			// No last downscale label set on the statefulset, we can continue
			continue
		}

		downscaleTime, err := time.Parse(time.RFC3339, lastDownscaleLabel)
		if err != nil {
			return false, err
		}

		timeBetweenDownscaleLabel, ok := sts.Labels[TimeBetweenZonesDownscaleAnnotationKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			continue
		}

		timeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			return false, err
		}

		if downscaleTime.Add(timeBetweenDownscale).After(time.Now().UTC()) {
			return true, nil
		}

	}
	return false, nil
}
