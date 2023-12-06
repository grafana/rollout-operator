package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/thanos-io/objstore"
	"github.com/thanos-io/objstore/providers/azure"
	"github.com/thanos-io/objstore/providers/gcs"
	"github.com/thanos-io/objstore/providers/s3"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
)

type zoneTracker struct {
	mu    sync.Mutex
	zones map[string]string
	bkt   objstore.Bucket
	key   string
}

// loadZones loads the zone file from object storage and populates the zone tracker.
// If the zone file does not exist, create it and upload it to the bucket then try to get it again.
func (zt *zoneTracker) loadZones(ctx context.Context, stsList *appsv1.StatefulSetList) error {
	r, err := zt.bkt.Get(ctx, zt.key)
	if err != nil {
		if zt.bkt.IsObjNotFoundErr(err) {
			// Create the zone file if it doesn't exist and populate with the current time for each zone, try to get it again
			err = zt.createInitialZones(ctx, stsList)
			if err != nil {
				return err
			}
			r, err = zt.bkt.Get(ctx, zt.key)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	defer r.Close()

	return json.NewDecoder(r).Decode(&zt.zones)
}

// saveZones encodes the zone tracker to json and uploads the zone file to the bucket.
func (zt *zoneTracker) saveZones(ctx context.Context) error {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(zt.zones); err != nil {
		return err
	}

	return zt.bkt.Upload(ctx, zt.key, buf)
}

// lastDownscaled returns the last time the zone was downscaled in UTC in time.RFC3339 format.
func (zt *zoneTracker) lastDownscaled(ctx context.Context, zone string) (string, error) {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	return zt.zones[zone], nil
}

// setDownscaled sets the last time the zone was downscaled to the current time in UTC in time.RFC3339 format.
func (zt *zoneTracker) setDownscaled(ctx context.Context, zone string) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	zt.zones[zone] = time.Now().UTC().Format(time.RFC3339)

	return zt.saveZones(ctx)
}

// findDownscalesDoneMinTimeAgo returns the statefulset that was downscaled the least amount of time ago
func (zt *zoneTracker) findDownscalesDoneMinTimeAgo(stsList *appsv1.StatefulSetList, stsName string) (*statefulSetDownscale, error) {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	for _, sts := range stsList.Items {
		if sts.Name == stsName {
			continue
		}
		lastDownscaleStr, ok := zt.zones[sts.Name]
		if !ok {
			// No last downscale timestamp set for the statefulset, we can continue
			continue
		}

		lastDownscale, err := time.Parse(time.RFC3339, lastDownscaleStr)
		if err != nil {
			return nil, fmt.Errorf("can't parse last downscale time of %s: %w", sts.Name, err)
		}

		timeBetweenDownscaleLabel, ok := sts.Labels[config.MinTimeBetweenZonesDownscaleLabelKey]
		if !ok {
			// No time between downscale label set on the statefulset, we can continue
			continue
		}

		minTimeBetweenDownscale, err := time.ParseDuration(timeBetweenDownscaleLabel)
		if err != nil {
			return nil, fmt.Errorf("can't parse %v label of %s: %w", config.MinTimeBetweenZonesDownscaleLabelKey, sts.Name, err)
		}

		if time.Since(lastDownscale) < minTimeBetweenDownscale {
			s := statefulSetDownscale{
				name:              sts.Name,
				waitTime:          minTimeBetweenDownscale,
				lastDownscaleTime: lastDownscale,
			}
			return &s, nil
		}

	}
	return nil, nil
}

// If the zone file does not exist, populate it with the current time for each zone and upload it to the bucket.
func (zt *zoneTracker) createInitialZones(ctx context.Context, stsList *appsv1.StatefulSetList) error {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	currentTime := time.Now().UTC().Format(time.RFC3339)
	for _, sts := range stsList.Items {
		if _, ok := zt.zones[sts.Name]; !ok {
			zt.zones[sts.Name] = currentTime
		}
	}

	return zt.saveZones(ctx)
}

func newZoneTracker(bkt objstore.Bucket, key string) *zoneTracker {
	return &zoneTracker{
		zones: make(map[string]string),
		bkt:   bkt,
		key:   key,
	}
}

// newBucketClient creates a new object storage bucket client based on the minimum provided configuration flags and provider
// Supported providers are s3, azure, and gcs
func newBucketClient(ctx context.Context, provider string, bucket string, endpoint string, region string, accountName string, logger log.Logger) (objstore.Bucket, error) {
	switch provider {
	case "s3":
		config, err := createS3ConfigYaml(bucket, endpoint, region)
		if err != nil {
			return nil, err
		}
		return newS3BucketClient(config, logger)
	case "azure":
		config, err := createAzureConfigYaml(bucket, accountName, endpoint)
		if err != nil {
			return nil, err
		}
		return newAzureBucketClient(config, logger)
	case "gcs":
		config, err := createGCSConfigYaml(ctx, bucket)
		if err != nil {
			return nil, err
		}
		return newGCSBucketClient(ctx, config, logger)
	default:
		return nil, fmt.Errorf("unable to provision an object storage bucket client based on the provided configuration flags")
	}
}

func newS3BucketClient(config []byte, logger log.Logger) (objstore.Bucket, error) {
	return s3.NewBucket(logger, config, "grafana-rollout-operator")
}

func newAzureBucketClient(config []byte, logger log.Logger) (objstore.Bucket, error) {
	return azure.NewBucket(logger, config, "grafana-rollout-operator")
}

func newGCSBucketClient(ctx context.Context, config []byte, logger log.Logger) (objstore.Bucket, error) {
	return gcs.NewBucket(ctx, logger, config, "grafana-rollout-operator")
}

func createS3ConfigYaml(bucket, endpoint, region string) ([]byte, error) {
	cfg := &s3.Config{
		Bucket:    bucket,
		Endpoint:  endpoint,
		Region:    region,
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func createAzureConfigYaml(container, accountName, endpoint string) ([]byte, error) {
	cfg := &azure.Config{
		ContainerName:      container,
		StorageAccountKey:  os.Getenv("AZURE_BLOB_SECRET_ACCESS_KEY"),
		StorageAccountName: accountName,
		Endpoint:           endpoint,
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func createGCSConfigYaml(ctx context.Context, bucket string) ([]byte, error) {
	cfg := &gcs.Config{
		Bucket: bucket,
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}

	return data, nil
}
