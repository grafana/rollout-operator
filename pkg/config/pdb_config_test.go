package config

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestMaxUnavailable validates the determination of the number of unavailable pods based on a fixed value config or a percentage of StatefulSet replicas
func TestMaxUnavailable(t *testing.T) {
	sts := newSts("test-sts")

	pdbCfg := PdbConfig{}
	require.Equal(t, 0, pdbCfg.MaxUnavailablePods(sts))

	pdbCfg.maxUnavailable = 1
	require.Equal(t, 1, pdbCfg.MaxUnavailablePods(sts))

	pdbCfg.maxUnavailable = 0
	pdbCfg.maxUnavailablePercentage = 100
	require.Equal(t, 3, pdbCfg.MaxUnavailablePods(sts))

	pdbCfg.maxUnavailablePercentage = 1
	require.Equal(t, 1, pdbCfg.MaxUnavailablePods(sts))

	pdbCfg.maxUnavailablePercentage = 75
	require.Equal(t, 2, pdbCfg.MaxUnavailablePods(sts))
}

// TestPodPartitionZoneMatch validates the regular expression parsing of a Pod name to return a logical partition name
func TestPodPartitionZoneMatch(t *testing.T) {
	pdbCfg := PdbConfig{
		podNamePartition:           regexp.MustCompile(`^[a-z\-]+-(zone-[a-z]-[0-9]+)$`),
		podNamePartitionRegexGroup: 1,
	}

	// test successful matches
	for _, name := range []string{"ingester-zone-a-0", "test-app-zone-a-0"} {
		pod := newPod(name)
		p, err := pdbCfg.PodPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "zone-a-0", p)
	}

	// test no match
	for _, name := range []string{"", "ingester-zone-a", "test-app-1"} {
		pod := newPod(name)
		_, err := pdbCfg.PodPartition(pod)
		require.NotNil(t, err)
	}
}

// TestPodPartitionZoneMatch validates the regular expression parsing of a Pod name to return a logical partition name
func TestPodPartitionZoneMatchWithGrouping(t *testing.T) {
	pdbCfg := PdbConfig{
		podNamePartition:           regexp.MustCompile(`^ingester(-foo)?-zone-[a-z]-([0-9]+)$`),
		podNamePartitionRegexGroup: 2,
	}

	// test successful matches
	for _, name := range []string{"ingester-foo-zone-a-0", "ingester-zone-a-0"} {
		pod := newPod(name)
		p, err := pdbCfg.PodPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "0", p)
	}
}

func TestBadRegex(t *testing.T) {
	raw := map[string]interface{}{}
	raw["podNamePartitionRegex"] = "(a bad regex["

	_, _, err := valueAsRegex(raw, "podNamePartitionRegex")
	require.ErrorContains(t, err, "error parsing regexp: missing closing ]: `[$`")

	raw["podNamePartitionRegex"] = "ingester-zone-[a-z]-[0-9]+"
	_, _, err = valueAsRegex(raw, "podNamePartitionRegex")
	require.ErrorContains(t, err, "regular expression requires at least one subexpression")

	raw["podNamePartitionRegex"] = "ingester-zone-([a-z])-([0-9]+)"
	_, _, err = valueAsRegex(raw, "podNamePartitionRegex")
	require.ErrorContains(t, err, "regular expression has multiple subexpressions and requires an ,$index suffix")

	raw["podNamePartitionRegex"] = "ingester-zone-([a-z])-([0-9]+),$foo"
	_, _, err = valueAsRegex(raw, "podNamePartitionRegex")
	require.ErrorContains(t, err, "regular expression has multiple subexpressions and requires an ,$index suffix")

	raw["podNamePartitionRegex"] = "ingester-zone-([a-z])-([0-9]+),$0"
	_, _, err = valueAsRegex(raw, "podNamePartitionRegex")
	fmt.Printf("%s\n", err.Error())
	require.ErrorContains(t, err, "regular expression subexpression index must be greater than 0")

	raw["podNamePartitionRegex"] = "ingester-zone-([a-z])-([0-9]+),$10"
	_, _, err = valueAsRegex(raw, "podNamePartitionRegex")
	fmt.Printf("%s\n", err.Error())
	require.ErrorContains(t, err, "regular expression subexpression index out of range")

}

func TestRegexGrouping(t *testing.T) {
	raw := map[string]interface{}{
		"podNamePartitionRegex": "ingester(-foo)?-zone-[a-z]-([0-9]+),$2",
	}
	regex, group, err := valueAsRegex(raw, "podNamePartitionRegex")
	require.NotNil(t, regex)
	require.Equal(t, 2, group)
	require.Nil(t, err)

	cfg := &PdbConfig{
		podNamePartitionRegexGroup: group,
		podNamePartition:           regex,
	}

	p, err := cfg.PodPartition(newPod("foo"))
	require.NotNil(t, err)

	p, err = cfg.PodPartition(newPod("ingester-foo-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)

	p, err = cfg.PodPartition(newPod("ingester-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)
}

// newSts returns a minimal StatefulSet which only has a name and replica count attributes set
func newSts(name string) *appsv1.StatefulSet {
	replicas := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
	}
}

// newPod returns a minimal Pod which only has a name set
func newPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}
