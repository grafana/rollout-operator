package controller

import (
	"testing"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/grafana/rollout-operator/pkg/config"
)

func TestGetMaxUnavailableForStatefulSet(t *testing.T) {
	tests := map[string]struct {
		sts      *v1.StatefulSet
		expected int
	}{
		"should return 1 if the annotation is not set": {
			sts:      &v1.StatefulSet{},
			expected: 1,
		},
		"should return the value from the annotation if set": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "3",
					},
				},
			},
			expected: 3,
		},
		"should return 1 if the value set in the annotation is not an integer": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "xxx",
					},
				},
			},
			expected: 1,
		},
		"should return 1 if the value set in the annotation is 0": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "0",
					},
				},
			},
			expected: 1,
		},
		"should return 1 if the value set in the annotation is negative": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "-1",
					},
				},
			},
			expected: 1,
		},
		"should return percentage of statefulset replicas, if annotation uses percentage": {
			sts: &v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Replicas: intPointer(10),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "30%",
					},
				},
			},
			expected: 3,
		},
		"should return floor percentage of statefulset replicas, if annotation uses percentage": {
			sts: &v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Replicas: intPointer(10),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "25%",
					},
				},
			},
			expected: 2,
		},
		"should return floor percentage of statefulset replicas, if annotation uses percentage, 3 replicas": {
			sts: &v1.StatefulSet{
				Spec: v1.StatefulSetSpec{
					Replicas: intPointer(3),
				},
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "50%",
					},
				},
			},
			expected: 1,
		},
		"should return 1 of statefulset replicas, if annotation uses percentage, but statefulset doesn't have replicas": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "30%",
					},
				},
			},
			expected: 1,
		},
		"should return 1 of statefulset replicas, if annotation uses percentage, but values is too high": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "300%",
					},
				},
			},
			expected: 1,
		},
		"should return 1 of statefulset replicas, if annotation uses percentage, but values is too low": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						config.RolloutMaxUnavailableAnnotationKey: "0%",
					},
				},
			},
			expected: 1,
		}}

	for testName, testCase := range tests {
		t.Run(testName, func(t *testing.T) {
			assert.Equal(t, testCase.expected, getMaxUnavailableForStatefulSet(testCase.sts, log.NewNopLogger()))
		})
	}
}

func intPointer(i int32) *int32 {
	return &i
}
