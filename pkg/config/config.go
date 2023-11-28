package config

import (
	"gopkg.in/yaml.v2"
)

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

	// ZoneTrackerKey is the key used to store the zone tracker data in object storage
	ZoneTrackerKey = "zone-tracker/last-downscaled.json"
)

type S3Config struct {
	Type   string `yaml:"type"`
	Config struct {
		Bucket    string `yaml:"bucket"`
		Endpoint  string `yaml:"endpoint"`
		Region    string `yaml:"region"`
		AccessKey string `yaml:"access_key"`
		SecretKey string `yaml:"secret_key"`
		Insecure  bool   `yaml:"insecure"`
	} `yaml:"config"`
}

func CreateS3ConfigYaml(s3Bucket, s3Endpoint, s3Region, s3AccessKey, s3SecretKey string, insecure bool) ([]byte, error) {
	cfg := S3Config{
		Type: "S3",
	}
	cfg.Config.Bucket = s3Bucket
	cfg.Config.Endpoint = s3Endpoint
	cfg.Config.Region = s3Region
	cfg.Config.AccessKey = s3AccessKey
	cfg.Config.SecretKey = s3SecretKey
	cfg.Config.Insecure = true

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	return data, nil
}
