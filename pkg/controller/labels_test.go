package controller

import (
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
						RolloutMaxUnavailableAnnotation: "3",
					},
				},
			},
			expected: 3,
		},
		"should return 1 if the value set in the annotation is not an integer": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						RolloutMaxUnavailableAnnotation: "xxx",
					},
				},
			},
			expected: 1,
		},
		"should return 1 if the value set in the annotation is 0": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						RolloutMaxUnavailableAnnotation: "0",
					},
				},
			},
			expected: 1,
		},
		"should return 1 if the value set in the annotation is negative": {
			sts: &v1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						RolloutMaxUnavailableAnnotation: "-1",
					},
				},
			},
			expected: 1,
		},
	}

	for testName, testCase := range tests {
		t.Run(testName, func(t *testing.T) {
			assert.Equal(t, testCase.expected, getMaxUnavailableForStatefulSet(testCase.sts, log.NewNopLogger()))
		})
	}
}
