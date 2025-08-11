package zpdb

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/grafana/rollout-operator/pkg/config"
)

const (
	// strings needed to find the custom resource - keep as const as easier to change if we update the custom resource definition
	pdbCustomResourceKind = config.ZoneAwarePodDisruptionBudgetName

	// default values
	defaultMaxUnavailable = 1

	// fields we read from the config map - keep as const as easier to change if we update the custom resource definition
	fieldMaxUnavailable           = "maxUnavailable"
	fieldMaxUnavailablePercentage = "maxUnavailablePercentage"
	fieldPodNamePartitionRegex    = "podNamePartitionRegex"
)

// A ZpdbConfig holds the configuration of a ZoneAwarePodDisruptionBudget custom resource.
type ZpdbConfig struct {
	name string

	generation int64

	// the max unavailable pods in another zone before we deny an eviction
	maxUnavailable int

	// the max unavailable pods in another zone before deny an eviction - percentage is relative to the StatefulSet replica count
	maxUnavailablePercentage int

	// a selector for finding the StatefulSet for each zone - ie match on rollout-group label test-app-zone-a
	stsSelector *labels.Selector

	// a regex for how we find the partition from the pod name test-app-zone-a-0 --> 0
	podNamePartition *regexp.Regexp

	// the group number in the regex to use for the partition name - default=1
	podNamePartitionRegexGroup int
}

func (c *ZpdbConfig) Name() string {
	return c.name
}

func (c *ZpdbConfig) Generation() int64 {
	return c.generation
}

// MatchesPod returns true if this PdbConfig label selector matches this pod
func (c *ZpdbConfig) MatchesPod(pod *corev1.Pod) bool {
	selector := *c.stsSelector
	return selector.Matches(labels.Set(pod.Labels))
}

// MatchesSts returns true if this PdbConfig label selector matches this pod
func (c *ZpdbConfig) MatchesSts(sts *appsv1.StatefulSet) bool {
	selector := *c.stsSelector
	return selector.Matches(labels.Set(sts.Labels))
}

// MaxUnavailablePods returns the number of allowed unavailable pods.
// When the max unavailable configuration is a percentage, the returned value is calculated off the StatefulSet's Spec.Replica count.
func (c *ZpdbConfig) MaxUnavailablePods(sts *appsv1.StatefulSet) int {
	if c.maxUnavailable > 0 {
		return c.maxUnavailable
	}

	if c.maxUnavailablePercentage > 0 && sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
		result := int(math.Floor(float64(c.maxUnavailablePercentage*int(*sts.Spec.Replicas)) / 100))
		if result < 1 {
			result = defaultMaxUnavailable
		}
		return result
	}

	return 0
}

// StsSelector returns the Selector which can be used to find the other StatefulSets which span all zones.
// Note that this can be nil
func (c *ZpdbConfig) StsSelector() *labels.Selector {
	return c.stsSelector
}

// PodPartition returns the partition name that a Pod covers.
// Note that if no podNamePartitionRegex has been set then an empty string will be returned.
func (c *ZpdbConfig) PodPartition(pod *corev1.Pod) (string, error) {
	if c.podNamePartition == nil {
		return "", nil
	}

	zone := c.podNamePartition.FindStringSubmatch(pod.Name)
	if len(zone) > c.podNamePartitionRegexGroup && len(zone[c.podNamePartitionRegexGroup]) > 0 {
		return zone[c.podNamePartitionRegexGroup], nil
	}
	return "", errors.New("failed to extract partition from pod name regular expression")
}

// valueAsRegex attempts to find a string in the given map and compile it to a Regexp.
// The given string can have a subexpression grouping for the regular expression. This is defined by a ",$<group>" suffix.
// An error is returned if the compile fails or the subexpression groupings are not valid.
// A nil is returned for the Regexp if there is no string in the map.
func valueAsRegex(config map[string]interface{}, field string) (*regexp.Regexp, int, error) {
	if val, found := config[field]; found && len(val.(string)) > 0 {
		groupingIndex := 1
		grpRgx := regexp.MustCompile(`^.+(,\$([0-9]+))$`)
		grpMatch := grpRgx.FindStringSubmatch(val.(string))
		grpSet := false
		if len(grpMatch) == 3 {
			groupingIndex, _ = strconv.Atoi(grpMatch[2])
			val = val.(string)[0 : len(val.(string))-len(grpMatch[1])]
			grpSet = true
		}

		var re *regexp.Regexp
		var err error
		if re, err = regexp.Compile("^" + val.(string) + "$"); err != nil {
			return nil, 0, err
		}

		numSubexp := re.NumSubexp()

		if numSubexp == 0 {
			// regex has no ()
			return nil, 0, errors.New("regular expression requires at least one subexpression")
		} else if numSubexp > 1 && !grpSet {
			// regex has multiple () but the index has not been set
			return nil, 0, errors.New("regular expression has multiple subexpressions and requires an ,$index suffix")
		} else if numSubexp < groupingIndex {
			// the index exceeds the number of groups
			return nil, 0, errors.New("regular expression subexpression index out of range")
		} else if groupingIndex == 0 {
			return nil, 0, errors.New("regular expression subexpression index must be greater than 0")
		} else {
			return re, groupingIndex, nil
		}
	}
	// no regex - this is ok
	return nil, 0, nil
}

// ParseAndValidate attempts to parse the given Unstructured to a ZpdbConfig.
// An error is returned if any configuration errors are found.
func ParseAndValidate(obj *unstructured.Unstructured) (*ZpdbConfig, error) {
	var mapSpec map[string]interface{}
	var err error
	if spec, found, err := unstructured.NestedMap(obj.Object, "spec"); err != nil {
		return nil, err
	} else if !found {
		return nil, errors.New("no spec found in unstructured object")
	} else if obj.GetKind() != pdbCustomResourceKind {
		return nil, fmt.Errorf("unexpected object kind - expecting %s", pdbCustomResourceKind)
	} else {
		mapSpec = spec
	}

	cfg := &ZpdbConfig{
		maxUnavailable: defaultMaxUnavailable,
		name:           obj.GetName(),
		generation:     obj.GetGeneration(),
	}

	// We favour the maxUnavailable value, taking the first value > 0
	// maxUnavailable == maxUnavailablePercentage == 0 has the same effect
	if val, found := mapSpec[fieldMaxUnavailable]; found && val != nil {
		cfg.maxUnavailable = int(val.(int64))
		if cfg.maxUnavailable < 0 {
			// fatal
			return nil, fmt.Errorf("invalid value - max unavailable must be 0 <= val - %d", cfg.maxUnavailable)
		}
	} else if val, found := mapSpec[fieldMaxUnavailablePercentage]; found && val != nil {
		cfg.maxUnavailablePercentage = int(val.(int64))
		cfg.maxUnavailable = 0
		if cfg.maxUnavailablePercentage < 0 || cfg.maxUnavailablePercentage > 100 {
			// fatal
			return nil, fmt.Errorf("invalid value - max unavailable percentage must be 0 <= val <= 100 - %d", cfg.maxUnavailablePercentage)
		}
	}

	if cfg.podNamePartition, cfg.podNamePartitionRegexGroup, err = valueAsRegex(mapSpec, fieldPodNamePartitionRegex); err != nil {
		return nil, fmt.Errorf("invalid value - regex is not valid: %v", err)
	}

	if mlMap, found, err := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); err != nil {
		return nil, fmt.Errorf("invalid value - selector is not valid: %v", err)
	} else if !found {
		return nil, fmt.Errorf("invalid value - selector is not found")
	} else {
		selector := labels.SelectorFromSet(mlMap)
		cfg.stsSelector = &selector
	}

	return cfg, nil
}
