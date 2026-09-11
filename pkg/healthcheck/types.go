package healthcheck

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Decision describes whether a workload controller may advance its rollout.
type Decision struct {
	ShouldPause  bool
	Reason       string
	RequeueAfter time.Duration
}

// Request carries the workload context needed to evaluate a rollout transition.
type Request struct {
	RolloutGroup      string
	StateKey          string
	Namespace         string
	TargetName        string
	TargetKind        string
	TargetLabels      map[string]string
	TargetAnnotations map[string]string
	EventTarget       runtime.Object
	CandidatePods     []*corev1.Pod
	StablePods        []*corev1.Pod
	BaselineTime      time.Time
	Now               time.Time
}
