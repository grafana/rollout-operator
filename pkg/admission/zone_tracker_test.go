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
	bkt  objstore.Bucket
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

func (b *mockBucket) IsObjNotFoundErr(err error) bool {
	return err.Error() == "object not found"
}

func (*mockBucket) Attributes(ctx context.Context, name string) (objstore.ObjectAttributes, error) {
	panic("unimplemented")
}

func (*mockBucket) Close() error {
	panic("unimplemented")
}

func (*mockBucket) Delete(ctx context.Context, name string) error {
	panic("unimplemented")
}

func (*mockBucket) Exists(ctx context.Context, name string) (bool, error) {
	panic("unimplemented")
}

func (*mockBucket) GetRange(ctx context.Context, name string, off int64, length int64) (io.ReadCloser, error) {
	panic("unimplemented")
}

func (*mockBucket) IsAccessDeniedErr(err error) bool {
	panic("unimplemented")
}

func (*mockBucket) Iter(ctx context.Context, dir string, f func(string) error, options ...objstore.IterOption) error {
	panic("unimplemented")
}

func (*mockBucket) Name() string {
	panic("unimplemented")
}

func TestZoneTracker(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

	zones := []string{"testzone", "testzone2", "testzone3"}
	stsList := &apps.StatefulSetList{
		Items: []apps.StatefulSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testzone",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testzone2",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testzone3",
				},
			},
		},
	}

	for _, zone := range zones {
		if err := zt.loadZones(ctx, stsList); err != nil {
			t.Fatalf("loadZones failed: %v", err)
		}

		// Test isDownscaled
		_, err := zt.lastDownscaled(zone)
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

		if err := zt.loadZones(ctx, stsList); err != nil {
			t.Fatalf("loadZones failed: %v", err)
		}

		// Test isDownscaled
		downscaled, err := zt.lastDownscaled(zone)
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
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

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

	if err := zt.loadZones(ctx, stsList); err != nil {
		t.Fatalf("loadZones failed: %v", err)
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

func TestLoadZonesCreatesInitialZones(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")
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

	// Try to load zones when the zone file does not exist
	err := zt.loadZones(ctx, stsList)
	if err != nil {
		t.Fatalf("loadZones failed: %v", err)
	}

	// Check if the zone file was created
	data, err := bkt.Get(ctx, "testkey")
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	readData, err := io.ReadAll(data)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if string(readData) == "" {
		t.Errorf("loadZones did not create initial zones")
	}
}

func TestLoadZonesEmptyBucket(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")
	stsList := &apps.StatefulSetList{}

	err := zt.loadZones(ctx, stsList)
	if err != nil {
		t.Fatalf("loadZones failed: %v", err)
	}

	if len(zt.zones) != 0 {
		t.Errorf("loadZones failed to populate initial zones in an empty bucket")
	}
}

func TestSetDownscaledNonExistentZone(t *testing.T) {
	ctx := context.Background()
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

	err := zt.setDownscaled(ctx, "nonexistentzone")
	if err == nil {
		t.Fatalf("setDownscaled failed: %v", err)
	}

	if _, ok := zt.zones["nonexistentzone"]; ok {
		t.Errorf("setDownscaled did not handle non-existent zone correctly")
	}
}

func TestLastDownscaledNonExistentZone(t *testing.T) {
	bkt := &mockBucket{bkt: objstore.NewInMemBucket(), data: make(map[string][]byte)}
	zt := newZoneTracker(bkt, "testkey")

	time, _ := zt.lastDownscaled("nonexistentzone")
	fmt.Printf("time: %v\n", time)
	if time != "" {
		t.Errorf("lastDownscaled did not handle non-existent zone correctly")
	}
}
