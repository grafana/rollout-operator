package controller

import (
	"time"

	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/grafana/rollout-operator/pkg/status"
)

type recordedBlockers struct {
	uid        types.UID
	generation int64
	revision   string
	blockers   []status.Blocker
}

func (c *RolloutController) recordRolloutBlockers(sts *v1.StatefulSet, blockers []status.Blocker) {
	c.blockersMu.Lock()
	defer c.blockersMu.Unlock()
	if len(blockers) == 0 {
		delete(c.rolloutBlockers, sts.Name)
		return
	}
	now := time.Now().UTC()
	for i := range blockers {
		blockers[i].ObservedAt = now
	}
	if c.rolloutBlockers == nil {
		c.rolloutBlockers = make(map[string]recordedBlockers)
	}
	c.rolloutBlockers[sts.Name] = recordedBlockers{uid: sts.UID, generation: sts.Generation, revision: sts.Status.UpdateRevision, blockers: blockers}
}

func (c *RolloutController) clearRolloutBlockers(sets []*v1.StatefulSet) {
	c.blockersMu.Lock()
	defer c.blockersMu.Unlock()
	for _, sts := range sets {
		delete(c.rolloutBlockers, sts.Name)
	}
}

func (c *RolloutController) pruneRolloutBlockers(sets []*v1.StatefulSet) {
	names := make(map[string]struct{}, len(sets))
	for _, sts := range sets {
		names[sts.Name] = struct{}{}
	}
	c.blockersMu.Lock()
	defer c.blockersMu.Unlock()
	for name := range c.rolloutBlockers {
		if _, ok := names[name]; !ok {
			delete(c.rolloutBlockers, name)
		}
	}
}

func (c *RolloutController) currentRolloutBlockers(sts *v1.StatefulSet) []status.Blocker {
	c.blockersMu.RLock()
	recorded, ok := c.rolloutBlockers[sts.Name]
	c.blockersMu.RUnlock()
	// A new rollout or recreated StatefulSet must not inherit a previous attempt's denials.
	if !ok || recorded.uid != sts.UID || recorded.generation != sts.Generation || recorded.revision != sts.Status.UpdateRevision {
		return nil
	}
	var out []status.Blocker
	for _, blocker := range recorded.blockers {
		if blocker.Pod != "" {
			pod, err := c.podLister.Pods(sts.Namespace).Get(blocker.Pod)
			if err != nil || pod.UID != blocker.PodUID || pod.Labels[v1.ControllerRevisionHashLabelKey] == sts.Status.UpdateRevision {
				continue
			}
		}
		out = append(out, blocker)
	}
	return out
}
