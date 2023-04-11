package admission

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const (
	LastDownscaleAnnotationKey = "last-prepared-for-downscale"
)

func addPreparedForDownscaleAnnotationToPod(ctx context.Context, api kubernetes.Interface, namespace, stsName string, podNr int) error {
	client := api.CoreV1().Pods(namespace)
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{
		"name":                               stsName,
		"statefulset.kubernetes.io/pod-name": fmt.Sprintf("%v-%v", stsName, podNr),
	}}
	pods, err := client.List(ctx,
		v1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
	if err != nil {
		return err
	}
	if len(pods.Items) != 1 {
		return fmt.Errorf("multiple or no pods found for statefulset %v and index %v", stsName, podNr)
	}

	pod := pods.Items[0]

	annotations := pod.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[LastDownscaleAnnotationKey] = time.Now().UTC().String()
	pod.SetAnnotations(annotations)

	_, err = client.Update(ctx, &pod, metav1.UpdateOptions{})
	return err
}
