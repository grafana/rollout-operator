package healthcheck

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	RolloutHealthCheckKind       = "RolloutHealthCheck"
	RolloutHealthChecksPlural    = "rollouthealthchecks"
	RolloutHealthChecksSpecGroup = "rollout-operator.grafana.com"
	RolloutHealthChecksVersion   = "v1"

	placeholderTargetMatchers = "${targetMatchers}"
	placeholderRange          = "${range}"
	placeholderCurrent        = "${current}"
	placeholderBaseline       = "${baseline}"

	defaultCurrentRange  = 5 * time.Minute
	defaultBaselineRange = 10 * time.Minute
	defaultQueryTimeout  = 10 * time.Second
	defaultQueryRetries  = 3
)

// FailureAction controls how a failed or no-data check affects the rollout.
type FailureAction string

const (
	ActionPause    FailureAction = "Pause"
	ActionWarn     FailureAction = "Warn"
	ActionDisabled FailureAction = "Disabled"
)

// Check is a single PromQL health check within a RolloutHealthCheck.
type Check struct {
	Name          string
	CurrentRange  time.Duration
	BaselineRange time.Duration
	QueryTimeout  time.Duration
	QueryRetries  int
	OnFailure     FailureAction
	OnNoData      FailureAction
	Disabled      bool
	Query         string
	SuccessQuery  string
}

// Config is the parsed RolloutHealthCheck custom resource.
type Config struct {
	Name          string
	Generation    int64
	Selector      labels.Selector
	PrometheusURL string
	Checks        []Check
}

// MatchesLabels returns true if this Config's selector matches the given labels.
func (c *Config) MatchesLabels(set labels.Set) bool {
	if c.Selector == nil {
		return false
	}
	return c.Selector.Matches(set)
}

// ParseAndValidate parses an unstructured RolloutHealthCheck into a Config.
func ParseAndValidate(obj *unstructured.Unstructured) (*Config, error) {
	if obj.GetKind() != RolloutHealthCheckKind {
		return nil, fmt.Errorf("unexpected object kind - expecting %s", RolloutHealthCheckKind)
	}

	cfg := &Config{
		Name:       obj.GetName(),
		Generation: obj.GetGeneration(),
	}

	prometheusURL, found, err := unstructured.NestedString(obj.Object, "spec", "prometheusURL")
	if err != nil {
		return nil, fmt.Errorf("invalid prometheusURL: %w", err)
	}
	if !found || strings.TrimSpace(prometheusURL) == "" {
		return nil, errors.New("invalid value: prometheusURL is required")
	}
	cfg.PrometheusURL = strings.TrimSpace(prometheusURL)
	parsedURL, err := url.Parse(cfg.PrometheusURL)
	if err != nil {
		return nil, fmt.Errorf("invalid value: prometheusURL is not a valid URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, errors.New("invalid value: prometheusURL scheme must be http or https")
	}
	if parsedURL.Host == "" {
		return nil, errors.New("invalid value: prometheusURL must include a host")
	}

	if mlMap, found, err := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); err != nil {
		return nil, fmt.Errorf("invalid value: selector is not valid, got %v", err)
	} else if !found || len(mlMap) == 0 {
		return nil, errors.New("invalid value: selector.matchLabels is required")
	} else {
		cfg.Selector = labels.SelectorFromSet(mlMap)
	}

	rawChecks, found, err := unstructured.NestedSlice(obj.Object, "spec", "checks")
	if err != nil {
		return nil, fmt.Errorf("invalid checks: %w", err)
	}
	if !found || len(rawChecks) == 0 {
		return nil, errors.New("invalid value: checks must contain at least one entry")
	}

	seenNames := map[string]struct{}{}
	for i, raw := range rawChecks {
		checkMap, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid value: checks[%d] must be an object", i)
		}
		check, err := parseCheck(checkMap, i)
		if err != nil {
			return nil, err
		}
		if _, exists := seenNames[check.Name]; exists {
			return nil, fmt.Errorf("invalid value: duplicate check name %q", check.Name)
		}
		seenNames[check.Name] = struct{}{}
		cfg.Checks = append(cfg.Checks, check)
	}

	return cfg, nil
}

func parseCheck(m map[string]interface{}, index int) (Check, error) {
	check := Check{
		CurrentRange:  defaultCurrentRange,
		BaselineRange: defaultBaselineRange,
		QueryTimeout:  defaultQueryTimeout,
		QueryRetries:  defaultQueryRetries,
		OnFailure:     ActionPause,
		OnNoData:      ActionPause,
	}

	name, ok := m["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return Check{}, fmt.Errorf("invalid value: checks[%d].name is required", index)
	}
	check.Name = strings.TrimSpace(name)

	query, ok := m["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return Check{}, fmt.Errorf("invalid value: checks[%d].query is required", index)
	}
	check.Query = query
	if !strings.Contains(check.Query, placeholderTargetMatchers) {
		return Check{}, fmt.Errorf("invalid value: checks[%d].query must contain %s", index, placeholderTargetMatchers)
	}
	if !strings.Contains(check.Query, placeholderRange) {
		return Check{}, fmt.Errorf("invalid value: checks[%d].query must contain %s", index, placeholderRange)
	}

	successQuery, ok := m["successQuery"].(string)
	if !ok || strings.TrimSpace(successQuery) == "" {
		return Check{}, fmt.Errorf("invalid value: checks[%d].successQuery is required", index)
	}
	check.SuccessQuery = successQuery
	if !strings.Contains(check.SuccessQuery, placeholderCurrent) {
		return Check{}, fmt.Errorf("invalid value: checks[%d].successQuery must contain %s", index, placeholderCurrent)
	}
	if !strings.Contains(check.SuccessQuery, placeholderBaseline) {
		return Check{}, fmt.Errorf("invalid value: checks[%d].successQuery must contain %s", index, placeholderBaseline)
	}

	if v, found := m["currentRange"]; found {
		d, err := parseDurationField(v, "currentRange", index)
		if err != nil {
			return Check{}, err
		}
		check.CurrentRange = d
	}
	if v, found := m["baselineRange"]; found {
		d, err := parseDurationField(v, "baselineRange", index)
		if err != nil {
			return Check{}, err
		}
		check.BaselineRange = d
	}
	if v, found := m["queryTimeout"]; found {
		d, err := parseDurationField(v, "queryTimeout", index)
		if err != nil {
			return Check{}, err
		}
		check.QueryTimeout = d
	}
	if v, found := m["queryRetries"]; found {
		n, ok := asInt(v)
		if !ok || n < 0 {
			return Check{}, fmt.Errorf("invalid value: checks[%d].queryRetries must be >= 0", index)
		}
		check.QueryRetries = n
	}
	if v, found := m["onFailure"]; found {
		action, err := parseFailureAction(v, "onFailure", index)
		if err != nil {
			return Check{}, err
		}
		check.OnFailure = action
	}
	if v, found := m["onNoData"]; found {
		action, err := parseFailureAction(v, "onNoData", index)
		if err != nil {
			return Check{}, err
		}
		check.OnNoData = action
	}
	if v, found := m["disabled"]; found {
		disabled, ok := v.(bool)
		if !ok {
			return Check{}, fmt.Errorf("invalid value: checks[%d].disabled must be a boolean", index)
		}
		check.Disabled = disabled
	}

	return check, nil
}

func parseDurationField(v interface{}, field string, index int) (time.Duration, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("invalid value: checks[%d].%s must be a duration string", index, field)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value: checks[%d].%s is not a valid duration: %w", index, field, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid value: checks[%d].%s must be > 0", index, field)
	}
	return d, nil
}

func parseFailureAction(v interface{}, field string, index int) (FailureAction, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid value: checks[%d].%s must be a string", index, field)
	}
	switch FailureAction(s) {
	case ActionPause, ActionWarn, ActionDisabled:
		return FailureAction(s), nil
	default:
		return "", fmt.Errorf("invalid value: checks[%d].%s must be Pause, Warn, or Disabled", index, field)
	}
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int:
		return n, true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}
