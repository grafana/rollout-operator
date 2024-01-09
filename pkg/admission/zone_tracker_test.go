package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/grafana/rollout-operator/pkg/config"
	apps "k8s.io/api/apps/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestZoneTracker(t *testing.T) {
	ctx := context.Background()

	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

	zones := []string{"testzone", "testzone2", "testzone3"}
	stsList := &appsv1.StatefulSetList{
		Items: []appsv1.StatefulSet{
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

		// Test lastDownscaled
		_, err := zt.lastDownscaled(zone)
		if err != nil {
			t.Fatalf("lastDownscaled failed: %v", err)
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

		// Test lastDownscaled
		downscaled, err := zt.lastDownscaled(zone)
		if err != nil {
			t.Fatalf("lastDownscaled failed: %v", err)
		}

		if downscaled == "" {
			t.Fatalf("lastDownscaled returned an empty string, want a timestamp")
		}
	}
}

func TestZoneTrackerFindDownscalesDoneMinTimeAgo(t *testing.T) {
	ctx := context.Background()
	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

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
	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

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
	cm, err := client.CoreV1().ConfigMaps("testnamespace").Get(ctx, "testconfigmap", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get ConfigMap failed: %v", err)
	}

	// Check the data for each zone
	for zone, data := range cm.Data {
		var zi zoneInfo
		err = json.Unmarshal([]byte(data), &zi)
		if err != nil {
			t.Fatalf("Unmarshal failed for zone %s: %v", zone, err)
		}

		if zi.LastDownscaled == "" {
			t.Errorf("LastDownscaled is empty for zone %s", zone)
		}
	}
}

func TestLoadZonesEmptyConfigMap(t *testing.T) {
	ctx := context.Background()
	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

	stsList := &apps.StatefulSetList{}

	err := zt.loadZones(ctx, stsList)
	if err != nil {
		t.Fatalf("loadZones failed: %v", err)
	}

	if len(zt.zones) != 0 {
		t.Errorf("loadZones failed to populate initial zones in an empty bucket")
	}
}

func TestSetDownscaled(t *testing.T) {
	ctx := context.Background()
	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create the configmap
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "testconfigmap",
			Namespace: "testnamespace",
		},
		Data: map[string]string{
			"testzone": `{"LastDownscaled":"2020-01-01T00:00:00Z"}`,
		},
	}
	_, err := client.CoreV1().ConfigMaps("testnamespace").Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create ConfigMap failed: %v", err)
	}

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

	// Test when zone does not exist in the map
	zone := "nonexistentzone"
	err = zt.setDownscaled(context.Background(), zone)
	if err != nil {
		t.Fatalf("setDownscaled failed: %v", err)
	}

	zoneInfo, ok := zt.zones[zone]
	if !ok {
		t.Errorf("setDownscaled did not create non-existent zone")
	} else {
		// Check that the LastDownscaled time was set correctly
		lastDownscaled, err := time.Parse(time.RFC3339, zoneInfo.LastDownscaled)
		if err != nil {
			t.Errorf("setDownscaled did not set LastDownscaled time correctly: %v", err)
		}
		if time.Since(lastDownscaled) > time.Second {
			t.Errorf("setDownscaled did not set LastDownscaled time to now")
		}
	}

	// Test when zone already exists in the map
	err = zt.setDownscaled(context.Background(), zone)
	if err != nil {
		t.Fatalf("setDownscaled failed: %v", err)
	}

	zoneInfo, ok = zt.zones[zone]
	if !ok {
		t.Errorf("setDownscaled did not update existing zone")
	} else {
		// Check that the LastDownscaled time was updated correctly
		lastDownscaled, err := time.Parse(time.RFC3339, zoneInfo.LastDownscaled)
		if err != nil {
			t.Errorf("setDownscaled did not update LastDownscaled time correctly: %v", err)
		}
		if time.Since(lastDownscaled) > time.Second {
			t.Errorf("setDownscaled did not update LastDownscaled time to now")
		}
	}
}

func TestLastDownscaledNonExistentZone(t *testing.T) {
	// Create a fake client
	client := fake.NewSimpleClientset()

	// Create a new zoneTracker with the fake client
	zt := newZoneTracker(client, "testnamespace", "testconfigmap")

	time, _ := zt.lastDownscaled("nonexistentzone")
	fmt.Printf("time: %v\n", time)
	if time != "" {
		t.Errorf("lastDownscaled did not handle non-existent zone correctly")
	}
}
