package config

const (
	// NoDownscaleLabelKey is the label to prevent downscaling of a statefulset
	NoDownscaleLabelKey   = "grafana.com/no-downscale"
	NoDownscaleLabelValue = "true"

	// LastDownscaleAnnotationKey is the last time the statefulset was scaled down in UTC in time.RFC3339 format.
	LastDownscaleAnnotationKey = "grafana.com/last-downscale"
	// MinTimeBetweenZonesDownscaleLabelKey is the minimum duration allowed between downscales of zones that are
	// part of the same rollout group in Go time.Duration format.
	MinTimeBetweenZonesDownscaleLabelKey = "grafana.com/min-time-between-zones-downscale"
	// PrepareDownscalePathAnnotationKey is the path to the endpoint on each pod that should be called when the
	// statefulset is being prepared to be scaled down.
	PrepareDownscalePathAnnotationKey = "grafana.com/prepare-downscale-http-path"
	// PrepareDownscalePortAnnotationKey is the port on each pod that should be used when the statefulset is being
	// prepared to be scaled down.
	PrepareDownscalePortAnnotationKey = "grafana.com/prepare-downscale-http-port"
	// PrepareDownscaleLabelKey is the label to prepare each pod in a statefulset when down scaling.
	PrepareDownscaleLabelKey   = "grafana.com/prepare-downscale"
	PrepareDownscaleLabelValue = "true"

	// RolloutGroupLabelKey is the group to which multiple statefulsets belong and must be operated on together.
	RolloutGroupLabelKey = "rollout-group"
	// RolloutMaxUnavailableAnnotationKey is the max number of pods in each statefulset that may be stopped at
	// one time.
	RolloutMaxUnavailableAnnotationKey = "rollout-max-unavailable"
	// RolloutDownscaleLeaderAnnotationKey is the name of the leader statefulset that should be used to determine
	// the number of replicas in a follower statefulset.
	RolloutDownscaleLeaderAnnotationKey = "grafana.com/rollout-downscale-leader"
	// RolloutLeaderReadyKey is whether to only scale up once `ready` replicas match the desired replicas.
	RolloutLeaderReadyAnnotationKey   = "grafana.com/rollout-upscale-only-when-leader-ready"
	RolloutLeaderReadyAnnotationValue = "true"

	rolloutMirrorReplicasFromResourceAnnotationKeyPrefix = "grafana.com/rollout-mirror-replicas-from-resource"
	// RolloutMirrorReplicasFromResourceNameAnnotationKey -- when set (together with "kind" and optionally "api-version" annotations), rollout-operator sets number of
	// replicas based on replicas in this resource (its scale subresource).
	RolloutMirrorReplicasFromResourceNameAnnotationKey       = rolloutMirrorReplicasFromResourceAnnotationKeyPrefix + "-name"
	RolloutMirrorReplicasFromResourceKindAnnotationKey       = rolloutMirrorReplicasFromResourceAnnotationKeyPrefix + "-kind"
	RolloutMirrorReplicasFromResourceAPIVersionAnnotationKey = rolloutMirrorReplicasFromResourceAnnotationKeyPrefix + "-api-version" // optional
	RolloutMirrorReplicasFromResourceWriteBackStatusReplicas = rolloutMirrorReplicasFromResourceAnnotationKeyPrefix + "-write-back"  // optional

	// RolloutDelayedDownscaleAnnotationKey configures delay for downscaling. Prepare-url must be configured as well, and must support GET, POST and DELETE methods.
	RolloutDelayedDownscaleAnnotationKey = "grafana.com/rollout-delayed-downscale"

	// RolloutDelayedDownscalePrepareUrlAnnotationKey is a full URL to prepare-downscale endpoint. Hostname will be replaced with pod's fully qualified domain name.
	RolloutDelayedDownscalePrepareUrlAnnotationKey = "grafana.com/rollout-prepare-delayed-downscale-url"

	// RolloutForceReplicasAnnotationKey overrides the operator's replica decisions.
	// When set to a non-negative integer, the operator will scale the StatefulSet to that
	// number of replicas, bypassing leader/follower and mirror-resource logic.
	// In leader/follower mode, scaling is immediate. In mirror-replicas mode, delayed
	// downscale is still respected if configured.
	RolloutForceReplicasAnnotationKey = "grafana.com/rollout-force-replicas"

	// RolloutPausedAnnotationKey pauses pod rollouts for a StatefulSet when set to RolloutPausedAnnotationValue.
	// The operator will skip deleting pods for that StatefulSet while allowing other
	// StatefulSets in the same rollout group to continue rolling out.
	RolloutPausedAnnotationKey   = "grafana.com/rollout-paused"
	RolloutPausedAnnotationValue = "true"

	// RolloutPhasedLabelKey opts a Deployment into phased (dependency-chained) rollouts.
	RolloutPhasedLabelKey   = "grafana.com/rollout-phased"
	RolloutPhasedLabelValue = "true"

	// RolloutDependsOnAnnotationKey names the upstream Deployment that must finish and soak
	// before this Deployment is allowed to roll.
	RolloutDependsOnAnnotationKey = "grafana.com/rollout-depends-on"

	// RolloutRevisionAnnotationKey is a shared revision stamp set by Git/jsonnet. The operator
	// only gates when this value changes, so zone-local template differences are ignored.
	RolloutRevisionAnnotationKey = "grafana.com/rollout-revision"

	// RolloutSoakDurationAnnotationKey overrides the default soak duration (Prometheus duration, e.g. "5m").
	RolloutSoakDurationAnnotationKey = "grafana.com/rollout-soak-duration"

	// RolloutRestartThresholdAnnotationKey overrides the default restart ratio threshold
	// (e.g. "10%" or "0.1") evaluated at the end of the soak.
	RolloutRestartThresholdAnnotationKey = "grafana.com/rollout-restart-threshold"

	// RolloutResumeAnnotationKey, when set to the gated revision, bypasses a failed soak gate
	// and immediately unpauses the Deployment.
	RolloutResumeAnnotationKey = "grafana.com/rollout-resume"

	// Operator-owned state annotations written by the webhook and phased Deployment controller.
	RolloutDependencyPhaseAnnotationKey    = "grafana.com/rollout-dependency-phase"
	RolloutDependencyRevisionAnnotationKey = "grafana.com/rollout-dependency-revision"
	RolloutDependencyReasonAnnotationKey   = "grafana.com/rollout-dependency-reason"
	RolloutSoakStartedAtAnnotationKey      = "grafana.com/rollout-soak-started-at"
	RolloutRestartBaselineAnnotationKey    = "grafana.com/rollout-restart-baseline"
	RolloutHadPausedAnnotationKey          = "grafana.com/rollout-had-paused"

	// Dependency phase values.
	RolloutDependencyPhaseWaiting  = "waiting"
	RolloutDependencyPhaseSoaking  = "soaking"
	RolloutDependencyPhaseBlocked  = "blocked"
	RolloutDependencyPhaseComplete = "complete"
)
