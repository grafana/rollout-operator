package config

import (
	"context"
	"fmt"
	"math"
	"regexp"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/spanlogger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	// strings needed to find the custom resource
	pdbCustomResourceKind       = "PodDisruptionZoneBudget"
	pdbCustomResourceNamePlural = "poddisruptionzonebudgets"
	pdbCustomResourceSpecGroup  = "rollout-operator.grafana.com"
	pdbCustomResourceVersion    = "v1"

	// default values
	defaultMaxUnavailable = 1

	// fields we read from the config map
	fieldMaxUnavailable           = "maxUnavailable"
	fieldMaxUnavailablePercentage = "maxUnavailablePercentage"
	fieldPodNamePartitionRegex    = "podNamePartitionRegex"
	fieldSelector                 = "selector"
	fieldSpec                     = "spec"

	// strings used in logging
	logConfigErrorMessage = "PodDisruptionZoneBudget configuration error"
	logMsg                = "msg"
	logReason             = "reason"
	logField              = "field"
	logDefaultValue       = "defaultValue"
	logError              = "error"
)

// A PdbConfig holds the configuration of a PodDisruptionZoneBudget custom resource.
// The custom resource is loaded by name through the kubernetes dynamic client and mapped to a PdbConfig.
// An example custom resource definition and custom resource file can be found in the development directory.
type PdbConfig struct {
	// the max unavailable pods in another zone before we deny an eviction
	maxUnavailable int

	// the max unavailable pods in another zone before deny an eviction - percentage is relative to the StatefulSet replica count
	maxUnavailablePercentage int

	// a selector for finding the StatefulSet for each zone - ie match on rollout-group label test-app-zone-a
	stsSelector *labels.Selector

	// a regex for how we find the partition from the pod name test-app-zone-a-0 --> 0
	podNamePartition *regexp.Regexp
}

// MaxUnavailablePods returns the number of allowed unavailable pods.
// When the max unavailable configuration is a percentage, the returned value is calculated off the StatefulSet's Spec.Replica count.
func (c *PdbConfig) MaxUnavailablePods(sts *appsv1.StatefulSet) int {
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
func (c *PdbConfig) StsSelector() *labels.Selector {
	return c.stsSelector
}

// PodPartition returns the partition name that a Pod covers.
// Note that if no podNamePartitionRegex has been set then an empty string will be returned.
func (c *PdbConfig) PodPartition(pod *corev1.Pod) string {
	if c.podNamePartition == nil {
		return ""
	}

	zone := c.podNamePartition.FindStringSubmatch(pod.Name)
	if len(zone) > 1 && len(zone[1]) > 0 {
		return zone[1]
	}
	return ""
}

// valueAsRegex attempts to find a string in the given map and compile it to a Regexp.
// An error is returned if the compile fails.
// A nil is returned for the Regexp if there is no string in the map.
func valueAsRegex(config map[string]interface{}, field string) (*regexp.Regexp, error) {
	if val, found := config[field]; found && len(val.(string)) > 0 {
		re, err := regexp.Compile("^" + val.(string) + "$")
		if err != nil {
			return nil, err
		}
		return re, nil
	}
	return nil, nil
}

// GetCustomResourceConfig attempts to load a PodDisruptionZoneBudget configuration for the given name and namespace.
// This name will most likely be the rollout-group name. ie "ingester"
func GetCustomResourceConfig(ctx context.Context, namespace string, name string, client dynamic.Interface, log *spanlogger.SpanLogger) (*PdbConfig, error) {

	gvr := schema.GroupVersionResource{
		Group:    pdbCustomResourceSpecGroup,
		Version:  pdbCustomResourceVersion,
		Resource: pdbCustomResourceNamePlural, // must be plural
	}

	// Use dynamic client to get the custom resource by name
	// The custom resource name provides a unique CR within the namespace
	unstructuredObj, err := client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})

	if err != nil {
		_ = level.Error(log).Log(logMsg, logConfigErrorMessage, logReason, err.Error())
		return nil, fmt.Errorf("unable to load %s config", pdbCustomResourceKind)
	}

	var tmpConfig map[string]interface{}
	if spec, found, err := unstructured.NestedMap(unstructuredObj.Object, fieldSpec); err == nil && found {
		tmpConfig = spec
	}

	PdbConfig := &PdbConfig{}

	// We favour the maxUnavailable value, taking the first value > 0
	// maxUnavailable == maxUnavailablePercentage == 0 has the same effect
	if val, found := tmpConfig[fieldMaxUnavailable]; found && val != nil {
		PdbConfig.maxUnavailable = int(val.(int64))
		if PdbConfig.maxUnavailable < 0 {
			PdbConfig.maxUnavailable = defaultMaxUnavailable
			_ = level.Error(log).Log(logMsg, logConfigErrorMessage, logReason, "value must be >= 0 - using default value", logField, fieldMaxUnavailable, logDefaultValue, PdbConfig.maxUnavailable)
		}
	} else if val, found := tmpConfig[fieldMaxUnavailablePercentage]; found && val != nil {
		PdbConfig.maxUnavailablePercentage = int(val.(int64))
		if PdbConfig.maxUnavailablePercentage < 0 || PdbConfig.maxUnavailablePercentage > 100 {
			PdbConfig.maxUnavailablePercentage = 0
			PdbConfig.maxUnavailable = defaultMaxUnavailable
			// Note that this log line includes that the maxUnavailable default value has been set
			_ = level.Error(log).Log(logMsg, logConfigErrorMessage, logReason, fmt.Sprintf("value must be 0 <= val <= 100 - using default %s value", fieldMaxUnavailable), logField, fieldMaxUnavailablePercentage, logDefaultValue, PdbConfig.maxUnavailable)
		}
	}

	// the impact of this being incorrectly set is that we will apply the PDB for the whole zone rather than the individual partitions
	PdbConfig.podNamePartition, err = valueAsRegex(tmpConfig, fieldPodNamePartitionRegex)
	if err != nil {
		_ = level.Error(log).Log(logMsg, logConfigErrorMessage, logReason, "value must be a regular expression - no default value available", logField, fieldPodNamePartitionRegex, logError, err)
	}

	// the impact here is we will fall back to looking for related StatefulSets by finding those with a matching rollout-group label
	if mlMap, found, err := unstructured.NestedStringMap(unstructuredObj.Object, fieldSpec, fieldSelector, "matchLabels"); err == nil && found {
		selector := labels.SelectorFromSet(mlMap)
		PdbConfig.stsSelector = &selector
	} else {
		_ = level.Error(log).Log(logMsg, logConfigErrorMessage, logReason, "value must be matchLabels selector - using default value", logField, fieldSelector, logError, err, logDefaultValue, RolloutGroupLabelKey+"=<rollout_group>")
	}

	return PdbConfig, nil
}
