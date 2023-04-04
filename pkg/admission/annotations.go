package admission

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func addDownscaledAnnotation(ctx context.Context, api kubernetes.Interface, namespace, stsName string) error {
	client := api.AppsV1().StatefulSets(namespace)
	sts, err := client.Get(ctx, stsName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := sts.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[DownscalingAnnotationKey] = DownscalingAnnotationValue
	sts.SetAnnotations(annotations)

	_, err = client.Update(ctx, sts, metav1.UpdateOptions{})
	return err
}
