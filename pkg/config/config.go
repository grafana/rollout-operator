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

	// PrepareDownscaleMinDelayBeforeShutdown is minimum duration between call to prepare-downscale endpoint, and when downscale is actually
	// performed.
	PrepareDownscaleMinDelayBeforeShutdown = "grafana.com/prepare-downscale-min-delay-before-shutdown"
	// LastPrepareDownscaleAnnotationKey is a timestamp when prepare-downscale was called last time on the pod. (UTC, time.RFC3339 format)
	LastPrepareDownscaleAnnotationKey = "grafana.com/last-prepare-downscale"

	// PrepareDownscaleDeleteEnabledAnnotationKey is boolean option that enables calling "DELETE /prepare-downscale"
	// when dowscale is canceled during "min delay" period.
	PrepareDownscaleDeleteEnabledAnnotationKey   = "grafana.com/delete-prepare-downscale-enabled"
	PrepareDownscaleDeleteEnabledAnnotationValue = "true"

	// RolloutGroupLabelKey is the group to which multiple statefulsets belong and must be operated on together.
	RolloutGroupLabelKey = "rollout-group"
	// RolloutMaxUnavailableAnnotationKey is the max number of pods in each statefulset that may be stopped at
	// one time.
	RolloutMaxUnavailableAnnotationKey = "rollout-max-unavailable"
	// RolloutDownscaleLeaderAnnotationKey is the name of the leader statefulset that should be used to determine
	// the number of replicas in a follower statefulset.
	RolloutDownscaleLeaderAnnotationKey = "grafana.com/rollout-downscale-leader"
)
