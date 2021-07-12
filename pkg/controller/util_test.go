package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSortStatefulSets(t *testing.T) {
	input := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	expected := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	sortStatefulSets(input)
	assert.Equal(t, expected, input)
}

func TestSortPods(t *testing.T) {
	input := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-11"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-10"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-20"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-1"}},
	}

	expected := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-0"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-10"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-11"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-20"}},
	}

	sortPods(input)
	assert.Equal(t, expected, input)
}

func TestMoveStatefulSetToFront(t *testing.T) {
	input := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	expected := []*v1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ingester-zone-c"}},
	}

	actual := moveStatefulSetToFront(input, input[1])
	assert.Equal(t, expected, actual)
}

func TestMax(t *testing.T) {
	assert.Equal(t, 1, max(1))
	assert.Equal(t, 3, max(0, 3, 2))
}

func TestMin(t *testing.T) {
	assert.Equal(t, 1, min(1))
	assert.Equal(t, 3, min(4, 3, 5))
}
