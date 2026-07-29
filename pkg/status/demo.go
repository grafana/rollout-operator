package status

import (
	"context"
	"time"
)

// DemoSnapshot returns a fixed snapshot covering the main UI states for local preview.
func DemoSnapshot() *Snapshot {
	return &Snapshot{
		Namespace:  "mimir-dev-01",
		ObservedAt: time.Now().UTC(),
		Groups: []Group{
			{
				Name:   "ingester",
				Phase:  PhaseProgressing,
				Reason: "1 of 3 pods updated",
				Members: []Member{
					{
						Name:            "ingester-zone-a",
						DesiredReplicas: 3,
						ReadyReplicas:   3,
						CurrentRevision: "ingester-7f9c2a",
						UpdateRevision:  "ingester-7f9c2a",
						UpdatedPods:     3,
						TotalPods:       3,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseComplete,
					},
					{
						Name:            "ingester-zone-b",
						DesiredReplicas: 3,
						ReadyReplicas:   2,
						CurrentRevision: "ingester-3b1e44",
						UpdateRevision:  "ingester-7f9c2a",
						UpdatedPods:     1,
						TotalPods:       3,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseProgressing,
						Reason:          "1 of 3 pods updated",
					},
					{
						Name:            "ingester-zone-c",
						DesiredReplicas: 3,
						ReadyReplicas:   3,
						CurrentRevision: "ingester-3b1e44",
						UpdateRevision:  "ingester-7f9c2a",
						UpdatedPods:     0,
						TotalPods:       3,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseProgressing,
						Reason:          "0 of 3 pods updated",
					},
				},
			},
			{
				Name:   "store-gateway",
				Phase:  PhasePaused,
				Reason: "rollout paused",
				Members: []Member{
					{
						Name:            "store-gateway-zone-a",
						DesiredReplicas: 2,
						ReadyReplicas:   2,
						CurrentRevision: "store-gateway-aa11bb",
						UpdateRevision:  "store-gateway-cc22dd",
						UpdatedPods:     0,
						TotalPods:       2,
						Paused:          true,
						UpdateStrategy:  "OnDelete",
						Phase:           PhasePaused,
						Reason:          "rollout paused",
					},
					{
						Name:            "store-gateway-zone-b",
						DesiredReplicas: 2,
						ReadyReplicas:   2,
						CurrentRevision: "store-gateway-aa11bb",
						UpdateRevision:  "store-gateway-cc22dd",
						UpdatedPods:     0,
						TotalPods:       2,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseProgressing,
						Reason:          "0 of 2 pods updated",
					},
				},
			},
			{
				Name:  "compactor",
				Phase: PhaseComplete,
				Members: []Member{
					{
						Name:            "compactor-zone-a",
						DesiredReplicas: 1,
						ReadyReplicas:   1,
						CurrentRevision: "compactor-99ee00",
						UpdateRevision:  "compactor-99ee00",
						UpdatedPods:     1,
						TotalPods:       1,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseComplete,
					},
				},
			},
			{
				Name:   "querier",
				Phase:  PhaseWaiting,
				Reason: "multiple StatefulSets have not-Ready pods",
				Members: []Member{
					{
						Name:            "querier-zone-a",
						DesiredReplicas: 3,
						ReadyReplicas:   1,
						CurrentRevision: "querier-111aaa",
						UpdateRevision:  "querier-222bbb",
						UpdatedPods:     2,
						TotalPods:       3,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseProgressing,
						Reason:          "2 of 3 pods updated",
					},
					{
						Name:            "querier-zone-b",
						DesiredReplicas: 3,
						ReadyReplicas:   2,
						CurrentRevision: "querier-111aaa",
						UpdateRevision:  "querier-222bbb",
						UpdatedPods:     0,
						TotalPods:       3,
						UpdateStrategy:  "OnDelete",
						Phase:           PhaseProgressing,
						Reason:          "0 of 3 pods updated",
					},
				},
			},
			{
				Name:   "alertmanager",
				Phase:  PhaseDegraded,
				Reason: "update strategy is RollingUpdate; OnDelete is required",
				Members: []Member{
					{
						Name:            "alertmanager-zone-a",
						DesiredReplicas: 2,
						ReadyReplicas:   2,
						CurrentRevision: "alertmanager-dead01",
						UpdateRevision:  "alertmanager-dead01",
						UpdatedPods:     2,
						TotalPods:       2,
						UpdateStrategy:  "RollingUpdate",
						Phase:           PhaseDegraded,
						Reason:          "update strategy is RollingUpdate; OnDelete is required",
					},
				},
			},
		},
	}
}

// DemoReader serves DemoSnapshot for local UI preview.
type DemoReader struct{}

// Snapshot implements Reader.
func (DemoReader) Snapshot(context.Context) (*Snapshot, error) {
	return DemoSnapshot(), nil
}
