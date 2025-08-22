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
	PodEvictionWebhookPath = "/admission/pod-eviction"

	ZoneAwarePodDisruptionBudgetName        = "ZoneAwarePodDisruptionBudget"
	ZoneAwarePodDisruptionBudgetsNamePlural = "zoneawarepoddisruptionbudgets"
	ZoneAwarePodDisruptionBudgetsSpecGroup  = "rollout-operator.grafana.com"
	ZoneAwarePodDisruptionBudgetsVersion    = "v1"

	// attributes in the zpdb yaml file
	FieldMaxUnavailable           = "maxUnavailable"
	FieldMaxUnavailablePercentage = "maxUnavailablePercentage"
	FieldPodNamePartitionRegex    = "podNamePartitionRegex"
	FieldPodNameRegexGroup        = "podNameRegexGroup"
	FieldSelector                 = "selector"
	FieldMatchLabels              = "matchLabels"
)

// A config holds the configuration of a ZoneAwarePodDisruptionBudget custom resource.
type config struct {
	name string

	generation int64

	// the max unavailable pods in another zone before we deny an eviction
	maxUnavailable int

	// the max unavailable pods in another zone before deny an eviction - percentage is relative to the StatefulSet replica count
	maxUnavailablePercentage int

	// a selector for finding the StatefulSet for each zone and for finding if a config applies to a pod - ie match on rollout-group label test-app-zone-a
	selector *labels.Selector

	// a regex for how we find the partition from the pod name test-app-zone-a-0 --> 0
	podNamePartition *regexp.Regexp

	// the group number in the regex to use for the partition name - default=1
	podNamePartitionRegexGroup int
}

// matchesPod returns true if this Config label Selector matches this Pod.
func (c *config) matchesPod(pod *corev1.Pod) bool {
	selector := *c.selector
	return selector.Matches(labels.Set(pod.Labels))
}

// maxUnavailablePods returns the number of allowed unavailable pods.
func (c *config) maxUnavailablePods(sts *appsv1.StatefulSet) int {
	if c.maxUnavailable > 0 {
		return c.maxUnavailable
	}

	if c.maxUnavailablePercentage > 0 && sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {

		// The min() is used to give the most conservative calculation as to the allowed number of unavailable pods.
		replicas := min(*sts.Spec.Replicas, sts.Status.Replicas)

		if replicas == 0 {
			return 0
		}

		result := int(math.Floor(float64(c.maxUnavailablePercentage*int(replicas)) / 100))
		if result < 1 {
			result = 1
		}
		return result
	}

	return 0
}

// podPartition returns the partition name that a Pod covers.
// Note that if no podNamePartitionRegex has been set then an empty string will be returned.
func (c *config) podPartition(pod *corev1.Pod) (string, error) {
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
// A nil is returned for the Regexp if there is no string in the map.
func valueAsRegex(config map[string]interface{}, regexField string, groupField string) (*regexp.Regexp, int, error) {
	var regexString string
	val, found := config[regexField]
	if !found {
		// no regex - this is ok
		return nil, 0, nil
	}

	regexString, ok := val.(string)
	if !ok {
		return nil, 0, fmt.Errorf("failed to extract regex value from config field %s", regexField)
	}

	if len(regexString) == 0 {
		// no regex - this is ok
		return nil, 0, nil
	}

	var groupValue int
	var groupValueSet = false
	val, found = config[groupField]
	if !found {
		groupValue = 1
	} else {
		tmp, ok := val.(int64)
		if !ok {
			return nil, 0, fmt.Errorf("failed to extract group value from config field %s", groupField)
		}
		groupValue = int(tmp)
		groupValueSet = true
	}

	re, err := regexp.Compile("^" + regexString + "$")
	if err != nil {
		return nil, 0, err
	}

	numSubExp := re.NumSubexp()

	if numSubExp == 0 {
		// regex has no subexpressions ()
		return nil, 0, errors.New("regular expression requires at least one subexpression")
	} else if numSubExp > 1 && !groupValueSet {
		// regex has multiple () but the index has not been set
		return nil, 0, fmt.Errorf("regular expression has multiple subexpressions and requires %s to be set", groupField)
	} else if numSubExp < groupValue {
		// the index exceeds the number of groups
		return nil, 0, errors.New("regular expression subexpression index out of range")
	}
	return re, groupValue, nil
}

// ParseAndValidate attempts to parse the given Unstructured to a Config.
// An error is returned if any configuration errors are found.
func ParseAndValidate(obj *unstructured.Unstructured) (*config, error) {
	mapSpec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return nil, err
	} else if !found {
		return nil, errors.New("no spec found in unstructured object")
	} else if obj.GetKind() != ZoneAwarePodDisruptionBudgetName {
		return nil, fmt.Errorf("unexpected object kind - expecting %s", ZoneAwarePodDisruptionBudgetName)
	}

	cfg := &config{
		maxUnavailable: 1,
		name:           obj.GetName(),
		generation:     obj.GetGeneration(),
	}

	maxUnavailable, maxUnavailableFound := mapSpec[FieldMaxUnavailable]
	maxUnavailableP, maxUnavailableFoundP := mapSpec[FieldMaxUnavailablePercentage]

	if maxUnavailableFound && maxUnavailableFoundP {
		return nil, errors.New("invalid value: only one of maxUnavailable or maxUnavailablePercentage may be set")
	}

	if maxUnavailableFound {

		tmp, ok := maxUnavailable.(int64)
		if !ok {
			return nil, fmt.Errorf("failed to extract value from config field %s", FieldMaxUnavailable)
		}
		cfg.maxUnavailable = int(tmp)
		if cfg.maxUnavailable < 0 {
			return nil, fmt.Errorf("invalid value: max unavailable must be 0 <= val, got %d", cfg.maxUnavailable)
		}
	} else if maxUnavailableFoundP {
		tmp, ok := maxUnavailableP.(int64)
		if !ok {
			return nil, fmt.Errorf("failed to extract value from config field %s", FieldMaxUnavailablePercentage)
		}
		cfg.maxUnavailablePercentage = int(tmp)
		cfg.maxUnavailable = 0
		if cfg.maxUnavailablePercentage < 0 || cfg.maxUnavailablePercentage > 100 {
			return nil, fmt.Errorf("invalid value: max unavailable percentage must be 0 <= val <= 100, got %d", cfg.maxUnavailablePercentage)
		}
	}

	if cfg.podNamePartition, cfg.podNamePartitionRegexGroup, err = valueAsRegex(mapSpec, FieldPodNamePartitionRegex, FieldPodNameRegexGroup); err != nil {
		return nil, fmt.Errorf("invalid value: regex is not valid, got %v", err)
	}

	if mlMap, found, err := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); err != nil {
		return nil, fmt.Errorf("invalid value: selector is not valid, got %v", err)
	} else if !found {
		return nil, fmt.Errorf("invalid value: selector is not found")
	} else {
		selector := labels.SelectorFromSet(mlMap)
		cfg.selector = &selector
	}

	if cfg.maxUnavailablePercentage > 0 && cfg.podNamePartition != nil {
		return nil, errors.New("invalid value: max unavailable percentage can not be used with partition awareness")
	}

	return cfg, nil
}
