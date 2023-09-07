# Changelog

## main / unreleased

* [ENHANCEMENT] Update Go to `1.21`. #77
* [ENHANCEMENT] Updated dependencies, including: #78
  * `github.com/k3d-io/k3d/v5` from `v5.5.2` to `v5.6.0` 
  * `k8s.io/api` from `v0.27.4` to `v0.28.1`
  * `k8s.io/apimachinery` from `v0.27.4` to `v0.28.1`
  * `k8s.io/client-go` from `v0.27.4` to `v0.28.1`

## v0.7.0

* [ENHANCEMENT] Updated dependencies, including: #70
  * `github.com/k3d-io/k3d/v5` from `v5.5.1` to `v5.5.2` 
  * `github.com/prometheus/client_golang` from `v1.15.1` to `v1.16.0`
  * `github.com/sirupsen/logrus` from `v1.9.2` to `v1.9.3`
  * `golang.org/x/sync` from `v0.2.0` to `v0.3.0`
  * `k8s.io/api` from `v0.27.2` to `v0.27.4`
  * `k8s.io/apimachinery` from `v0.27.2` to `v0.27.4`
  * `k8s.io/client-go` from `v0.27.2` to `v0.27.4`

## v0.6.1

* [FEATURE] Publish an additional boringcrypto image for linux/amd64,linux/arm64. #71
* [ENHANCEMENT] Update the intermediate build container for the Docker image to `golang:1.20-alpine3.18`. #71

## v0.6.0

* [ENHANCEMENT] Update Go to `1.20.4`. #55
* [ENHANCEMENT] Update Docker base image to `alpine:3.18`. #56
* [ENHANCEMENT] Add the ability to scale statefulsets up and down based on the number of replicas in a "leader" statefulset. #62
* [ENHANCEMENT] Updated dependencies, including: #63
  * `github.com/k3d-io/k3d/v5` from `v5.4.9` to `v5.5.1`
  * `github.com/prometheus/client_golang` from `v1.15.0` to `v1.15.1`
  * `github.com/prometheus/common` from `v0.42.0` to `v0.44.0`
  * `github.com/sirupsen/logrus` from `v1.9.0` to `v1.9.2`
  * `github.com/stretchr/testify` from `v1.8.2` to `v1.8.4`
  * `go.uber.org/atomic` from `v1.10.0` to `v1.11.0`
  * `golang.org/x/sync` from `v0.1.0` to `v0.2.0`
  * `k8s.io/api` from `v0.26.2` to `v0.27.2`
  * `k8s.io/apimachinery` from `v0.26.2` to `v0.27.2`
  * `k8s.io/client-go` from `v0.26.2` to `v0.27.2`

## v0.5.0

* [ENHANCEMENT] Add an `/admission/prepare-downscale` mutating admission webhook that prepares pods for shutdown before downscaling. #47 #52
* [ENHANCEMENT] Update Go to `1.20.3`. #48
* [ENHANCEMENT] Updated dependencies, including: #49
  * `github.com/k3d-io/k3d/v5` from `v5.4.7` to `v5.4.9`
  * `github.com/prometheus/client_golang` from `v1.14.0` to `v1.15.0`
  * `github.com/prometheus/common` from `v0.39.0` to `v0.42.0`
  * `github.com/stretchr/testify` from `v1.8.1` to `v1.8.2`
  * `go.uber.org/atomic` from `v1.9.0` to `v1.10.0`
  * `k8s.io/api` from `v0.26.1` to `v0.26.2`
  * `k8s.io/apimachinery` from `v0.26.1` to `v0.26.2`
  * `k8s.io/client-go` from `v0.26.1` to `v0.26.2`

## v0.4.0

* [ENHANCEMENT] Update Go to `1.20.1`. #39
* [ENHANCEMENT] Updated dependencies, including: #39 #42
  * `github.com/k3d-io/k3d/v5` from `v5.4.6` to `v5.4.7`
  * `github.com/prometheus/client_golang` from `v1.13.0` to `v1.14.0`
  * `github.com/prometheus/common` from `v0.37.0` to `v0.39.0`
  * `github.com/stretchr/testify` from `v1.8.0` to `v1.8.1`
  * `k8s.io/api` from `v0.25.3` to `v0.26.1`
  * `k8s.io/apimachinery` from `v0.25.3` to `v0.26.1`
  * `k8s.io/client-go` from `v0.25.3` to `v0.26.1`
  * `github.com/containerd/containerd` from `v1.6.15` to `v1.6.18` for CVE-2023-25153 and CVE-2023-25173
  * `golang.org/x/net` from `v0.5.0` to `v0.8.0` for CVE-2022-41723
* [ENHANCEMENT] Update Docker base image to `alpine:3.17`. #40
* [ENHANCEMENT] The image published is now a linux/amd64,linux/arm64 multi-platform image. #40

## v0.3.0

* [ENHANCEMENT] Update Go to `1.19.4`. #29
* [ENHANCEMENT] Added `-reconcile.interval` option to configure the minimum reconciliation interval. #30
* [ENHANCEMENT] Update dependencies to address CVEs, including: #34
  * `github.com/containerd/containerd` from `v1.6.8` to `v1.6.15` for CVE-2022-23471
  * `golang.org/x/net` from `v0.1.0` to `v0.5.0` for CVE-2022-41717
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
