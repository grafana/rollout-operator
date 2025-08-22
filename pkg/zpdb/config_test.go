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

	rolloutconfig "github.com/grafana/rollout-operator/pkg/config"
)

// TestMaxUnavailable validates the determination of the number of unavailable pods based on a fixed value config or a percentage of StatefulSet replicas
func TestMaxUnavailable(t *testing.T) {
	sts := newSts("test-sts")

	pdbCfg := config{}
	require.Equal(t, 0, pdbCfg.maxUnavailablePods(sts))

	pdbCfg.maxUnavailable = 1
	require.Equal(t, 1, pdbCfg.maxUnavailablePods(sts))

	pdbCfg.maxUnavailable = 0
	pdbCfg.maxUnavailablePercentage = 100
	require.Equal(t, 3, pdbCfg.maxUnavailablePods(sts))

	pdbCfg.maxUnavailablePercentage = 1
	require.Equal(t, 1, pdbCfg.maxUnavailablePods(sts))

	pdbCfg.maxUnavailablePercentage = 75
	require.Equal(t, 2, pdbCfg.maxUnavailablePods(sts))

	// test that we use the min of these 2 replica values
	pdbCfg.maxUnavailablePercentage = 50
	replicas := int32(10)
	sts.Spec.Replicas = &replicas
	sts.Status.Replicas = 6
	require.Equal(t, 3, pdbCfg.maxUnavailablePods(sts))

	sts.Spec.Replicas = &replicas
	sts.Status.Replicas = 20
	require.Equal(t, 5, pdbCfg.maxUnavailablePods(sts))

	sts.Status.Replicas = 0
	require.Equal(t, 0, pdbCfg.maxUnavailablePods(sts))
}

// TestPodPartitionZoneMatch validates the regular expression parsing of a Pod name to return a logical partition name
func TestPodPartitionZoneMatch(t *testing.T) {
	pdbCfg := config{
		podNamePartition:           regexp.MustCompile(`^[a-z\-]+-(zone-[a-z]-[0-9]+)$`),
		podNamePartitionRegexGroup: 1,
	}

	// test successful matches
	for _, name := range []string{"ingester-zone-a-0", "test-app-zone-a-0"} {
		pod := newPodCfgTest(name)
		p, err := pdbCfg.podPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "zone-a-0", p)
	}

	// test no match
	for _, name := range []string{"", "ingester-zone-a", "test-app-1"} {
		pod := newPodCfgTest(name)
		_, err := pdbCfg.podPartition(pod)
		require.NotNil(t, err)
	}
}

// TestPodPartitionZoneMatch validates the regular expression parsing of a Pod name to return a logical partition name
func TestPodPartitionZoneMatchWithGrouping(t *testing.T) {
	pdbCfg := config{
		podNamePartition:           regexp.MustCompile(`^ingester(-foo)?-zone-[a-z]-([0-9]+)$`),
		podNamePartitionRegexGroup: 2,
	}

	// test successful matches
	for _, name := range []string{"ingester-foo-zone-a-0", "ingester-zone-a-0"} {
		pod := newPodCfgTest(name)
		p, err := pdbCfg.podPartition(pod)
		require.NoError(t, err)
		require.Equal(t, "0", p)
	}
}

func TestBadRegex(t *testing.T) {
	raw := map[string]interface{}{}
	raw[FieldPodNamePartitionRegex] = "(a bad regex["

	_, _, err := valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	require.ErrorContains(t, err, "error parsing regexp: missing closing ]: `[$`")

	raw[FieldPodNamePartitionRegex] = "ingester-zone-[a-z]-[0-9]+"
	_, _, err = valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	require.ErrorContains(t, err, "regular expression requires at least one subexpression")

	raw[FieldPodNamePartitionRegex] = "ingester-zone-([a-z])-([0-9]+)"
	_, _, err = valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	require.ErrorContains(t, err, "regular expression has multiple subexpressions and requires podNameRegexGroup to be set")

	raw[FieldPodNameRegexGroup] = int64(10)
	raw[FieldPodNamePartitionRegex] = "ingester-zone-([a-z])-([0-9]+),$10"
	_, _, err = valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	fmt.Printf("%s\n", err.Error())
	require.ErrorContains(t, err, "regular expression subexpression index out of range")
}

func TestRegexGroupingDefault(t *testing.T) {
	raw := map[string]interface{}{
		FieldPodNamePartitionRegex: "ingester-zone-[a-z]-([0-9]+)",
	}
	regex, group, err := valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	require.NotNil(t, regex)
	require.Equal(t, 1, group)
	require.NoError(t, err)
}

func TestRegexGrouping(t *testing.T) {
	raw := map[string]interface{}{
		FieldPodNamePartitionRegex: "ingester(-foo)?-zone-[a-z]-([0-9]+)",
		FieldPodNameRegexGroup:     int64(2),
	}
	regex, group, err := valueAsRegex(raw, FieldPodNamePartitionRegex, FieldPodNameRegexGroup)
	require.NotNil(t, regex)
	require.Equal(t, 2, group)
	require.NoError(t, err)

	cfg := &config{
		podNamePartitionRegexGroup: group,
		podNamePartition:           regex,
	}

	_, err = cfg.podPartition(newPodCfgTest("foo"))
	require.NotNil(t, err)

	p, err := cfg.podPartition(newPodCfgTest("ingester-foo-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)

	p, err = cfg.podPartition(newPodCfgTest("ingester-zone-a-1"))
	require.NoError(t, err)
	require.Equal(t, "1", p)
}

func TestParseAndValidate(t *testing.T) {
	name := "test-zpdb"
	rolloutGroup := "test"
	_, err := ParseAndValidate(rawConfigInvalidKind())
	require.ErrorContains(t, err, "unexpected object kind - expecting "+ZoneAwarePodDisruptionBudgetName)

	_, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(-1), ""))
	require.ErrorContains(t, err, "invalid value: max unavailable must be 0 <= val")

	_, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(-1), ""))
	require.ErrorContains(t, err, "invalid value: max unavailable percentage must be 0 <= val <= 100")

	_, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(101), ""))
	require.ErrorContains(t, err, "invalid value: max unavailable percentage must be 0 <= val <= 100")

	_, err = ParseAndValidate(rawConfigMaxUnavailablePercentage(name, rolloutGroup, int64(1), int64(50), "[a-z]+\\-([0-9]+)"))
	require.ErrorContains(t, err, "invalid value: max unavailable percentage can not be used with partition awareness")

	_, err = ParseAndValidate(rawConfigMultipleUnavailable(name, rolloutGroup, int64(1), int64(50), int64(1)))
	require.ErrorContains(t, err, "invalid value: only one of maxUnavailable or maxUnavailablePercentage may be set")

	_, err = ParseAndValidate(rawConfig(name, rolloutGroup, int64(1), int64(1), "([bad_regex"))
	require.ErrorContains(t, err, "invalid value: regex is not valid")

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
		Status: appsv1.StatefulSetStatus{
			Replicas: replicas,
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
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  testNamespace,
				"generation": generation,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable: maxUnavailable,
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
				FieldPodNamePartitionRegex: podNamePartitionRegex,
			},
		},
	}
}

func rawConfigMaxUnavailablePercentage(name string, rolloutGroup string, generation int64, maxUnavailablePercentage int64, podNamePartitionRegex string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  testNamespace,
				"generation": generation,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailablePercentage: maxUnavailablePercentage,
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
				FieldPodNamePartitionRegex: podNamePartitionRegex,
			},
		},
	}
}

func rawConfigMultipleUnavailable(name string, rolloutGroup string, generation int64, maxUnavailablePercentage int64, maxUnavailable int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       ZoneAwarePodDisruptionBudgetName,
			"metadata": map[string]interface{}{
				"name":       name,
				"namespace":  testNamespace,
				"generation": generation,
			},
			"spec": map[string]interface{}{
				FieldMaxUnavailable:           maxUnavailable,
				FieldMaxUnavailablePercentage: maxUnavailablePercentage,
				FieldSelector: map[string]interface{}{
					FieldMatchLabels: map[string]interface{}{
						rolloutconfig.RolloutGroupLabelKey: rolloutGroup,
					},
				},
			},
		},
	}
}

func rawConfigInvalidKind() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": fmt.Sprintf("%s/%s", ZoneAwarePodDisruptionBudgetsSpecGroup, ZoneAwarePodDisruptionBudgetsVersion),
			"kind":       "Foo",
			"spec":       map[string]interface{}{},
		},
	}
}
