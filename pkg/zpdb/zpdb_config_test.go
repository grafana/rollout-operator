package zpdb

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/grafana/rollout-operator/pkg/config"
)

// TestMaxUnavailable validates the determination of the number of unavailable pods based on a fixed value config or a percentage of StatefulSet replicas
func TestMaxUnavailable(t *testing.T) {
	sts := newSts("test-sts")

	pdbCfg := ZpdbConfig{}
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
	pdbCfg := ZpdbConfig{
		podNamePartition:           regexp.MustCompile(`^[a-z\-]+-(zone-[a-z]-[0-9]+)$`),
		podNamePartitionRegexGroup: 1,
	}

	// test successful matches
	for _, name := range []string{"ingester-zone-a-0", "test-app-zone-a-0"} {
		pod := newPodCfgTest(name)
		p, err := pdbCfg.PodPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "zone-a-0", p)
	}

	// test no match
	for _, name := range []string{"", "ingester-zone-a", "test-app-1"} {
		pod := newPodCfgTest(name)
		_, err := pdbCfg.PodPartition(pod)
		require.NotNil(t, err)
	}
}

// TestPodPartitionZoneMatch validates the regular expression parsing of a Pod name to return a logical partition name
func TestPodPartitionZoneMatchWithGrouping(t *testing.T) {
	pdbCfg := ZpdbConfig{
		podNamePartition:           regexp.MustCompile(`^ingester(-foo)?-zone-[a-z]-([0-9]+)$`),
		podNamePartitionRegexGroup: 2,
	}

	// test successful matches
	for _, name := range []string{"ingester-foo-zone-a-0", "ingester-zone-a-0"} {
		pod := newPodCfgTest(name)
		p, err := pdbCfg.PodPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "0", p)
	}
}

func TestBadRegex(t *testing.T) {
	raw := map[string]interface{}{}
	raw[fieldPodNamePartitionRegex] = "(a bad regex["

	_, _, err := valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	require.ErrorContains(t, err, "error parsing regexp: missing closing ]: `[$`")

	raw[fieldPodNamePartitionRegex] = "ingester-zone-[a-z]-[0-9]+"
	_, _, err = valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	require.ErrorContains(t, err, "regular expression requires at least one subexpression")

	raw[fieldPodNamePartitionRegex] = "ingester-zone-([a-z])-([0-9]+)"
	_, _, err = valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	require.ErrorContains(t, err, "regular expression has multiple subexpressions and requires an ,$index suffix")

	raw[fieldPodNameRegexGroup] = int64(10)
	raw[fieldPodNamePartitionRegex] = "ingester-zone-([a-z])-([0-9]+),$10"
	_, _, err = valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	fmt.Printf("%s\n", err.Error())
	require.ErrorContains(t, err, "regular expression subexpression index out of range")
}

func TestRegexGroupingDefault(t *testing.T) {
	raw := map[string]interface{}{
		fieldPodNamePartitionRegex: "ingester-zone-[a-z]-([0-9]+)",
	}
	regex, group, err := valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	require.NotNil(t, regex)
	require.Equal(t, 1, group)
	require.Nil(t, err)
}

func TestRegexGrouping(t *testing.T) {
	raw := map[string]interface{}{
		fieldPodNamePartitionRegex: "ingester(-foo)?-zone-[a-z]-([0-9]+)",
		fieldPodNameRegexGroup:     int64(2),
	}
	regex, group, err := valueAsRegex(raw, fieldPodNamePartitionRegex, fieldPodNameRegexGroup)
	require.NotNil(t, regex)
	require.Equal(t, 2, group)
	require.Nil(t, err)

	cfg := &ZpdbConfig{
		podNamePartitionRegexGroup: group,
		podNamePartition:           regex,
	}

	_, err = cfg.PodPartition(newPodCfgTest("foo"))
	require.NotNil(t, err)

	p, err := cfg.PodPartition(newPodCfgTest("ingester-foo-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)

	p, err = cfg.PodPartition(newPodCfgTest("ingester-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)
}

func TestParseAndValidate(t *testing.T) {
	name := "test-zpdb"
	rolloutGroup := "test"
	_, err := ParseAndValidate(rawConfigInvalidKind())
	require.ErrorContains(t, err, "unexpected object kind - expecting ZoneAwarePodDisruptionBudget")

	_, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(-1), ""))
	require.ErrorContains(t, err, "invalid value - max unavailable must be 0 <= val")

	_, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(-1), ""))
	require.ErrorContains(t, err, "invalid value - max unavailable percentage must be 0 <= val <= 100")

	_, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(101), ""))
	require.ErrorContains(t, err, "invalid value - max unavailable percentage must be 0 <= val <= 100")

	_, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(1), "([bad_regex"))
	require.ErrorContains(t, err, "invalid value - regex is not valid")

	cfg, err := ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(0), ""))
	require.NoError(t, err)
	require.Equal(t, cfg.maxUnavailable, 0)

	cfg, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(1), ""))
	require.NoError(t, err)
	require.Equal(t, cfg.maxUnavailable, 1)

	cfg, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(0), ""))
	require.NoError(t, err)
	require.Equal(t, cfg.maxUnavailablePercentage, 0)

	cfg, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(100), ""))
	require.NoError(t, err)
	require.Equal(t, cfg.maxUnavailablePercentage, 100)

	// simple regex test - other tests more extensively validate the regex parsing
	cfg, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(1), "[a-z]+\\-([0-9]+)"))
	require.NoError(t, err)
	require.Equal(t, cfg.maxUnavailable, 1)
	require.NotNil(t, cfg.podNamePartitionRegexGroup)
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
func newPodCfgTest(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func rawConfig(name string, rolloutGroup string, generation int64, maxUnavailable int64, podNamePartitionRegex string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  "testnamespace",
				"generation": generation,
			},
			"spec": map[string]interface{}{
				"maxUnavailable": maxUnavailable,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						config.RolloutGroupLabelKey: rolloutGroup,
					},
				},
				"podNamePartitionRegex": podNamePartitionRegex,
			},
		},
	}
}

func rawConfigMaxUnavailablePercentage(name string, rolloutGroup string, generation int64, maxUnavailablePercentage int64, podNamePartitionRegex string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "ZoneAwarePodDisruptionBudget",
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  "testnamespace",
				"generation": generation,
			},
			"spec": map[string]interface{}{
				"maxUnavailablePercentage": maxUnavailablePercentage,
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						config.RolloutGroupLabelKey: rolloutGroup,
					},
				},
				"podNamePartitionRegex": podNamePartitionRegex,
			},
		},
	}
}

func rawConfigInvalidKind() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "rollout-operator.grafana.com/v1",
			"kind":       "Foo",
			"spec":       map[string]interface{}{},
		},
	}
}
