# Changelog

## main / unreleased

* [ENHANCEMENT] Update Go to `1.19.4`. #29
* [ENHANCEMENT] Added `-reconcile.interval` option to configure the minimum reconciliation interval. #30  
* [ENHANCEMENT] Update dependencies to address CVEs, including: #34
  * `github.com/containerd/containerd` from `v1.6.8` to `v1.6.15` for CVE-2022-23471
  * `golang.org/x/net` from `v0.1.0` to `v0.5.0` for CVE-20222-41717
* [FEATURE] `ValidatingAdmissionWebhook` implementation to prevent downscales on certain resources. #25

## v0.2.0

* [ENHANCEMENT] Update Docker base image to `alpine:3.16`. #23
* [ENHANCEMENT] Update the minimum go version to `1.18`. #23
* [ENHANCEMENT] Update dependencies to address CVEs, including: #23
  * `github.com/gogo/protobuf` from `v1.3.1` to `v1.3.2` 
  * `github.com/prometheus/client_golang` from `v1.11.0` to `v1.13.0`
  * `golang.org/x/crypto` from `v0.0.0-20200622213623-75b288015ac9` to `v0.1.0`
  * `k8s.io/client-go` from `v0.18.17` to `v0.25.3`

## v0.1.2

* [ENHANCEMENT] Improve log messages about not read statefulset. #19
* [BUGFIX] Detect as not-ready pods which are terminating but their readiness probe hasn't failed yet. #20

## v0.1.1

* [BUGFIX] Fix metrics deletion on decommissioned rollout group. #11

## v0.1.0

* [FEATURE] Coordinate rollout of pods belonging to multiple StatefulSets identified by the same `rollout-group` label. #1 #5 #6
* [FEATURE] Expose Prometheus metrics at `/metrics`. #4
* [FEATURE] Add readiness probe at `/ready`. #6
