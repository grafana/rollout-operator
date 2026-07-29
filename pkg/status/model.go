package status

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrUnavailable is returned when the status reader is not ready yet.
var ErrUnavailable = errors.New("status unavailable")

// Phase is a normalized rollout state for a group or StatefulSet member.
type Phase string

const (
	PhaseComplete    Phase = "complete"
	PhaseProgressing Phase = "progressing"
	PhaseWaiting     Phase = "waiting"
	PhasePaused      Phase = "paused"
	PhaseDegraded    Phase = "degraded"
	PhaseUnknown     Phase = "unknown"
)

// Reader provides a read-only snapshot of rollout status from informer caches.
type Reader interface {
	Snapshot(ctx context.Context) (*Snapshot, error)
}

// Snapshot is a point-in-time view of managed rollouts in a namespace.
type Snapshot struct {
	Namespace  string
	ObservedAt time.Time
	Groups     []Group
}

// Group is the status of one rollout-group of StatefulSets.
type Group struct {
	Name    string
	Phase   Phase
	Reason  string
	Members []Member
}

// Member is the status of one StatefulSet within a rollout group.
type Member struct {
	Name            string
	DesiredReplicas int32
	ReadyReplicas   int32
	CurrentRevision string
	UpdateRevision  string
	UpdatedPods     int
	TotalPods       int
	Paused          bool
	// NotReady matches reconcile's not-ready detection (Status.Replicas / pod readiness),
	// not Spec.Replicas, so scale-up is not treated as a multi-zone rollout block.
	NotReady       bool
	UpdateStrategy string
	Phase          Phase
	Reason         string
}

// Holder is a Reader that can be bound after informer initialization.
type Holder struct {
	mu sync.RWMutex
	r  Reader
}

// Set binds the underlying reader. Safe for concurrent use with Snapshot.
func (h *Holder) Set(r Reader) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.r = r
}

// Snapshot implements Reader.
func (h *Holder) Snapshot(ctx context.Context) (*Snapshot, error) {
	h.mu.RLock()
	r := h.r
	h.mu.RUnlock()
	if r == nil {
		return nil, ErrUnavailable
	}
	return r.Snapshot(ctx)
}
