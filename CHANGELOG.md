# Changelog

## main / unreleased

## v.0.1.2

* [ENHANCEMENT] Improve log messages about not read statefulset. #19
* [BUGFIX] Detect as not-ready pods which are terminating but their readiness probe hasn't failed yet. #20

## v0.1.1

* [BUGFIX] Fix metrics deletion on decommissioned rollout group. #11

## v0.1.0

* [FEATURE] Coordinate rollout of pods belonging to multiple StatefulSets identified by the same `rollout-group` label. #1 #5 #6
* [FEATURE] Expose Prometheus metrics at `/metrics`. #4
* [FEATURE] Add readiness probe at `/ready` #6.
