package zpdb

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	ZoneAwarePodDisruptionBudgetName        = "ZoneAwarePodDisruptionBudget"
	ZoneAwarePodDisruptionBudgetsNamePlural = "zoneawarepoddisruptionbudgets"
	ZoneAwarePodDisruptionBudgetsSpecGroup  = "rollout-operator.grafana.com"
	ZoneAwarePodDisruptionBudgetsVersion    = "v1"

	// fields we read from the config map - keep as const as easier to change if we update the custom resource definition
	fieldMaxUnavailable           = "maxUnavailable"
	fieldMaxUnavailablePercentage = "maxUnavailablePercentage"
	fieldPodNamePartitionRegex    = "podNamePartitionRegex"
	fieldPodNameRegexGroup        = "podNameRegexGroup"
)

// A Config holds the configuration of a ZoneAwarePodDisruptionBudget custom resource.
type Config struct {
	Name string

	Generation int64

	// the max unavailable pods in another zone before we deny an eviction
	maxUnavailable int

	// the max unavailable pods in another zone before deny an eviction - percentage is relative to the StatefulSet replica count
	maxUnavailablePercentage int

	// a selector for finding the StatefulSet for each zone - ie match on rollout-group label test-app-zone-a
	Selector *labels.Selector

	// a regex for how we find the partition from the pod name test-app-zone-a-0 --> 0
	podNamePartition *regexp.Regexp

	// the group number in the regex to use for the partition name - default=1
	podNamePartitionRegexGroup int
}

// MatchesPod returns true if this PdbConfig label Selector matches this pod
func (c *Config) MatchesPod(pod *corev1.Pod) bool {
	selector := *c.Selector
	return selector.Matches(labels.Set(pod.Labels))
}

// MatchesStatefulSet returns true if this PdbConfig label Selector matches this pod
func (c *Config) MatchesStatefulSet(sts *appsv1.StatefulSet) bool {
	selector := *c.Selector
	return selector.Matches(labels.Set(sts.Labels))
}

// MaxUnavailablePods returns the number of allowed unavailable pods.
// When the max unavailable configuration is a percentage, the returned value is calculated off the StatefulSet's Spec.Replica count.
func (c *Config) MaxUnavailablePods(sts *appsv1.StatefulSet) int {
	if c.maxUnavailable > 0 {
		return c.maxUnavailable
	}

	if c.maxUnavailablePercentage > 0 && sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
		result := int(math.Floor(float64(c.maxUnavailablePercentage*int(*sts.Spec.Replicas)) / 100))
		if result < 1 {
			result = 1
		}
		return result
	}

	return 0
}

// PodPartition returns the partition Name that a Pod covers.
// Note that if no podNamePartitionRegex has been set then an empty string will be returned.
func (c *Config) PodPartition(pod *corev1.Pod) (string, error) {
	if c.podNamePartition == nil {
		return "", nil
	}

	zone := c.podNamePartition.FindStringSubmatch(pod.Name)
	if len(zone) > c.podNamePartitionRegexGroup && len(zone[c.podNamePartitionRegexGroup]) > 0 {
		return zone[c.podNamePartitionRegexGroup], nil
	}
	return "", errors.New("failed to extract partition from pod Name regular expression")
}

// valueAsRegex attempts to find a string in the given map and compile it to a Regexp.
// The given string can have a subexpression grouping for the regular expression. This is defined by a ",$<group>" suffix.
// An error is returned if the compile fails or the subexpression groupings are not valid.
// A nil is returned for the Regexp if there is no string in the map.
func valueAsRegex(config map[string]interface{}, regexField string, groupField string) (*regexp.Regexp, int, error) {
	var regexString string
	if val, found := config[regexField]; !found || len(val.(string)) == 0 {
		// no regex - this is ok
		return nil, 0, nil
	} else {
		regexString = val.(string)
	}

	var groupValue int
	var groupValueSet = false
	if val, found := config[groupField]; !found {
		groupValue = 1
	} else {
		// note that the CRD constrains this to be >= 1
		groupValue = int(val.(int64))
		groupValueSet = true
	}

	re, err := regexp.Compile("^" + regexString + "$")
	if err != nil {
		return nil, 0, err
	}

	numSubexp := re.NumSubexp()

	if numSubexp == 0 {
		// regex has no subexpressions ()
		return nil, 0, errors.New("regular expression requires at least one subexpression")
	} else if numSubexp > 1 && !groupValueSet {
		// regex has multiple () but the index has not been set
		return nil, 0, fmt.Errorf("regular expression has multiple subexpressions and requires %s to be set", groupField)
	} else if numSubexp < groupValue {
		// the index exceeds the number of groups
		return nil, 0, errors.New("regular expression subexpression index out of range")
	}
	return re, groupValue, nil
}

// ParseAndValidate attempts to parse the given Unstructured to a Config.
// An error is returned if any configuration errors are found.
func ParseAndValidate(obj *unstructured.Unstructured) (*Config, error) {
	mapSpec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return nil, err
	} else if !found {
		return nil, errors.New("no spec found in unstructured object")
	} else if obj.GetKind() != ZoneAwarePodDisruptionBudgetName {
		return nil, fmt.Errorf("unexpected object kind - expecting %s", ZoneAwarePodDisruptionBudgetName)
	}

	cfg := &Config{
		maxUnavailable: 1,
		Name:           obj.GetName(),
		Generation:     obj.GetGeneration(),
	}

	maxUnavailable, maxUnavailableFound := mapSpec[fieldMaxUnavailable]
	maxUnavailableP, maxUnavailableFoundP := mapSpec[fieldMaxUnavailablePercentage]

	if maxUnavailableFound && maxUnavailableFoundP {
		return nil, errors.New("invalid value: only one of maxUnavailable or maxUnavailablePercentage may be set")
	}

	if maxUnavailableFound {
		cfg.maxUnavailable = int(maxUnavailable.(int64))
		if cfg.maxUnavailable < 0 {
			// fatal
			return nil, fmt.Errorf("invalid value: max unavailable must be 0 <= val, got %d", cfg.maxUnavailable)
		}
	} else if maxUnavailableFoundP {
		cfg.maxUnavailablePercentage = int(maxUnavailableP.(int64))
		cfg.maxUnavailable = 0
		if cfg.maxUnavailablePercentage < 0 || cfg.maxUnavailablePercentage > 100 {
			// fatal
			return nil, fmt.Errorf("invalid value: max unavailable percentage must be 0 <= val <= 100, got %d", cfg.maxUnavailablePercentage)
		}
	}

	if cfg.podNamePartition, cfg.podNamePartitionRegexGroup, err = valueAsRegex(mapSpec, fieldPodNamePartitionRegex, fieldPodNameRegexGroup); err != nil {
		return nil, fmt.Errorf("invalid value: regex is not valid, got %v", err)
	}

	if mlMap, found, err := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); err != nil {
		return nil, fmt.Errorf("invalid value: selector is not valid, got %v", err)
	} else if !found {
		return nil, fmt.Errorf("invalid value: selector is not found")
	} else {
		selector := labels.SelectorFromSet(mlMap)
		cfg.Selector = &selector
	}

	if cfg.maxUnavailablePercentage > 0 && cfg.podNamePartition != nil {
		return nil, errors.New("invalid value: max unavailable percentage can not be used with partition awareness")
	}

	return cfg, nil
}
