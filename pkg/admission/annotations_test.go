package admission

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAddDownscaledAnnotation(t *testing.T) {
	ctx := context.Background()
	namespace := "stsnamespace"
	stsName := "stsname"
	sts := []runtime.Object{
		&apps.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: "apps/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      stsName,
				Namespace: namespace,
				UID:       types.UID(stsName),
			},
		},
	}
	api := fake.NewSimpleClientset(sts...)

	err := addDownscaledAnnotation(ctx, api, namespace, stsName)
	require.NoError(t, err)

	updatedSts, err := api.AppsV1().StatefulSets(namespace).Get(ctx, stsName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedSts.Annotations)
	require.Equal(t, updatedSts.Annotations[DownscalingAnnotationKey], DownscalingAnnotationValue)
}
