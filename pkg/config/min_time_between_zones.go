package config

import (
	"fmt"
	"sync"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// warnedMinTimeLabelObjects tracks objects for which we've already logged the
// deprecated-label warning, to avoid spamming on every reconcile/admission check.
var warnedMinTimeLabelObjects sync.Map

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
			warnKey := obj.GetNamespace() + "/" + obj.GetName()
			if _, alreadyWarned := warnedMinTimeLabelObjects.LoadOrStore(warnKey, struct{}{}); !alreadyWarned {
				level.Warn(logger).Log(
					"msg", fmt.Sprintf("%s is set as a label; prefer the annotation with the same key. Label support will be removed in a future release", MinTimeBetweenZonesDownscaleLabelKey),
					"name", obj.GetName(),
					"namespace", obj.GetNamespace(),
				)
			}
			return value, true
		}
	}

	return "", false
}
