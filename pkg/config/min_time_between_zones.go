package config

import (
	"fmt"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// warnMinTimeLabelOnce ensures we only log the deprecated-label warning once
// for the process lifetime, to avoid spamming on every reconcile/admission check.
var warnMinTimeLabelOnce sync.Once

// GetMinTimeBetweenZonesDownscale returns the configured minimum time between zone
// downscales from the object's metadata. The annotation is preferred. The label is
// still accepted for migration and emits a warning when used.
func GetMinTimeBetweenZonesDownscale(obj metav1.Object, logger log.Logger) (string, bool) {
	if annotations := obj.GetAnnotations(); annotations != nil {
		if value, ok := annotations[MinTimeBetweenZonesDownscaleAnnotationKey]; ok && value != "" {
			return value, true
		}
	}

	if labels := obj.GetLabels(); labels != nil {
		if value, ok := labels[MinTimeBetweenZonesDownscaleLabelKey]; ok && value != "" {
			warnMinTimeLabelOnce.Do(func() {
				level.Warn(logger).Log(
					"msg", fmt.Sprintf("%s is set as a label; prefer the annotation with the same key. Label support will be removed in a future release", MinTimeBetweenZonesDownscaleLabelKey),
					"name", obj.GetName(),
					"namespace", obj.GetNamespace(),
				)
			})
			return value, true
		}
	}

	return "", false
}
