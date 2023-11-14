package admission

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/grafana/rollout-operator/pkg/config"
	"github.com/thanos-io/objstore"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type mockBucket struct {
	objstore.Bucket
	data map[string][]byte
}

func (b *mockBucket) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	data, ok := b.data[name]
	if !ok {
		return nil, fmt.Errorf("object not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *mockBucket) Upload(ctx context.Context, name string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.data[name] = data
	return nil
}

func TestZoneTracker(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

	zones := []string{"testzone", "testzone2", "testzone3"}
	initialData := fmt.Sprintf("{\"testzone\": \"%s\"}", time.Now().Format(time.RFC3339))

	if err := bkt.Upload(ctx, "testkey", bytes.NewBufferString(initialData)); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	for _, zone := range zones {
		if err := zt.loadZones(ctx); err != nil {
			t.Fatalf("loadZones failed: %v", err)
		}

		// Test isDownscaled
		_, err := zt.lastDownscaled(ctx, zone)
		if err != nil {
			t.Fatalf("isDownscaled failed: %v", err)
		}

		// Test setDownscaled
		if err := zt.setDownscaled(ctx, zone); err != nil {
			t.Fatalf("setDownscaled failed: %v", err)
		}

		// Test saveZones and loadZones
		if err := zt.saveZones(ctx); err != nil {
			t.Fatalf("saveZones failed: %v", err)
		}

		if err := zt.loadZones(ctx); err != nil {
			t.Fatalf("loadZones failed: %v", err)
		}

		// Test isDownscaled
		downscaled, err := zt.lastDownscaled(ctx, zone)
		if err != nil {
			t.Fatalf("isDownscaled failed: %v", err)
		}

		if downscaled == "" {
			t.Fatalf("isDownscaled returned false, want true")
		}
	}
}

func TestZoneTrackerFindDownscalesDoneMinTimeAgo(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

	// Create an initial file in the bucket
	initialData := fmt.Sprintf("{\"test-zone\": \"%s\"}", time.Now().Add(-time.Hour).Format(time.RFC3339))
	if err := bkt.Upload(ctx, "testkey", bytes.NewBufferString(initialData)); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	if err := zt.loadZones(ctx); err != nil {
		t.Fatalf("loadZones failed: %v", err)
	}

	stsList := &apps.StatefulSetList{
		Items: []apps.StatefulSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-zone",
					Labels: map[string]string{
						config.MinTimeBetweenZonesDownscaleLabelKey: "2h",
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "other-zone",
					Labels: map[string]string{
						config.MinTimeBetweenZonesDownscaleLabelKey: "1h",
					},
				},
			},
		},
	}

	s, err := zt.findDownscalesDoneMinTimeAgo(stsList, "other-zone")
	if err != nil {
		t.Fatalf("findDownscalesDoneMinTimeAgo failed: %v", err)
	}

	if s == nil {
		t.Fatalf("findDownscalesDoneMinTimeAgo returned nil, want statefulSetDownscale")
	}

	if s.name != "test-zone" {
		t.Errorf("findDownscalesDoneMinTimeAgo returned statefulSetDownscale with name %s, want test-zone", s.name)
	}

	if s.waitTime != 2*time.Hour {
		t.Errorf("findDownscalesDoneMinTimeAgo returned statefulSetDownscale with waitTime %v, want 2h", s.waitTime)
	}
}
