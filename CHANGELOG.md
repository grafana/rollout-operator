# Changelog

## main / unreleased

* [ENHANCEMENT] Dashboard: Support native histograms. #338 
* [ENHANCEMENT] Updated dependencies, including: #329 #335
  * `github.com/prometheus/common` from `v0.67.1` to `v0.67.3`
  * `golang.org/x/sync` from `v0.17.0` to `v0.18.0`
  * `k8s.io/api` from `v0.34.1` to `v0.34.2`
  * `k8s.io/apiextensions-apiserver` from `v0.34.1` to `v0.34.2`
  * `k8s.io/apimachinery` from `v0.34.1` to `v0.34.2`
  * `k8s.io/client-go` from `v0.34.1` to `v0.34.2`
  * `sigs.k8s.io/controller-runtime` from `v0.22.3` to `v0.22.4`

## v0.32.0

* [ENHANCEMENT] Check zone-aware PodDisruptionBudget compliance before deleting Pods during rolling updates. #324

## v0.31.1

* [ENHANCEMENT] Updated dependencies, including: #314, #315
  * `github.com/prometheus/common` from `v0.66.1` to `v0.67.1`
  * `sigs.k8s.io/controller-runtime` from `v0.22.0` to `v0.22.3`

## v0.31.0

* [ENHANCEMENT] Add further information to debug logs emitted when processing ZonedPodDisruptionBudget-related requests. #311 
* [BUGFIX] Keep evicted pod in eviction cache until its phase changes from running, rather than evicting on a pod ContainerStatuses change. Increase pod eviction cache TTL. #312

## v0.30.0

* [ENHANCEMENT] Add rollout-operator libsonnet into this repository. Enable rollout-operator and its webhooks by default. #282
* [ENHANCEMENT] Add rollout-operator dashboard mixin into this repository. #288
* [ENHANCEMENT] Add a `kubernetes.cluster-domain` flag for configuring a Kubernetes cluster domain. The default cluster domain `cluster.local.` is unchanged. #266
* [ENHANCEMENT] Updated dependencies, including: #280 #285 #291
  * `github.com/prometheus/client_golang` from `v1.23.0` to `v1.23.2`
  * `github.com/prometheus/common` from `v0.65.0` to `v0.66.1`
  * `go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace` from `v0.62.0` to `v0.63.0`
  * `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` from `v0.62.0` to `v0.63.0`
  * `go.opentelemetry.io/otel/trace` from `v1.37.0` to `v1.38.0`
  * `go.opentelemetry.io/otel` from `v1.37.0` to `v1.38.0`
  * `golang.org/x/sync` from `v0.16.0` to `v0.17.0`
  * `k8s.io/api` from `v0.33.3` to `v0.34.1`
  * `k8s.io/apiextensions-apiserver` from `v0.33.0` to `v0.34.1`
  * `k8s.io/apimachinery` from `v0.33.3` to `v0.34.1`
  * `k8s.io/client-go` from `v0.33.3` to `v0.34.1`
  * `sigs.k8s.io/controller-runtime` from `v0.21.0` to `v0.22.1`

## v0.29.0

* [CHANGE] Rename flag `server.cluster-validation.http.exclude-paths` to `server.cluster-validation.http.excluded-paths` to align with `dskit`. #247
* [ENHANCEMENT] Update Go to `1.25` #263
* [ENHANCEMENT] Updated dependencies, including: #236 #238 #242 #247 #257
  * `github.com/prometheus/client_golang` from `v1.22.0` to `v1.23.0`
  * `github.com/prometheus/common` from `v0.64.0` to `v0.65.0`
  * `go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace` from `v0.60.0` to `v0.62.0`
  * `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` from `v0.60.0` to `v0.62.0`
  * `go.opentelemetry.io/otel` from `v1.36.0` to `v1.37.0`
  * `go.opentelemetry.io/otel/trace` from `v1.36.0` to `v1.37.0`
  * `golang.org/x/sync` from `v0.15.0` to `v0.16.0`
  * `k8s.io/api` from `v0.33.1` to `v0.33.3`
  * `k8s.io/apimachinery` from `v0.33.1` to `v0.33.3`
  * `k8s.io/client-go` from `v0.33.1` to `v0.33.3`
* [ENHANCEMENT] Automatically patch validating and mutating rollout-operator webhooks with the self-signed CA if they are created or modified after the rollout-operator starts. #262, #273
* [ENHANCEMENT] Support for zone and partition aware pod disruption budgets, enabling finer control over pod eviction policies. #253
* [BUGFIX] Always configure HTTP client with a timeout. #240
* [BUGFIX] Use a StatefulSet's `.spec.serviceName` when constructing the delayed downscale endpoint for a pod. #258

## v0.28.0

* [ENHANCEMENT] Updated dependencies, including: #227 #231
  * `golang.org/x/sync` from `v0.14.0` to `v0.15.0`
  * `sigs.k8s.io/controller-runtime` from `v0.20.4` to `v0.21.0`
* [ENHANCEMENT] Migrate to OpenTelemetry tracing library, removing the dependency on OpenTracing. You can now configure tracing using the standard `OTEL_` [environment variables](https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/#batch-span-processor). Previous configurations using `JAEGER_` environment variables will still work, but are deprecated. #234

## v0.27.0

* [CHANGE] Rename metric `rollout_operator_request_invalid_cluster_validation_labels_total` to `rollout_operator_client_invalid_cluster_validation_label_requests_total`. #217
* [ENHANCEMENT] Add metric `rollout_operator_server_invalid_cluster_validation_label_requests_total`. #223
* [ENHANCEMENT] Updated dependencies, including: #223 #224
  * `github.com/prometheus/client_golang` from `v1.21.1` to `v1.22.0`
  * `github.com/prometheus/common` from `v0.63.0` to `v0.64.0`
  * `golang.org/x/sync` from `v0.12.0` to `v0.14.0`
  * `k8s.io/api` from `v0.32.3` to `v0.33.1`
  * `k8s.io/apimachinery` from `v0.32.3` to `v0.33.1`
  * `k8s.io/client-go` from `v0.32.3` to `v0.33.1`
* [BUGFIX] Use a StatefulSet's `.spec.serviceName` when constructing the prepare-downscale endpoint for a pod. #221

## v0.26.0

* [FEATURE] Add cross-cluster traffic protection. #195b
  * Controlled through the flags `-server.cluster-validation.http.enabled`, `-server.cluster-validation.http.soft-validation`, `-server.cluster-validation.http.exclude-paths`.
  * Rejected requests can be monitored via the metric `rollout_operator_request_invalid_cluster_validation_labels_total`.

## v0.25.0

* [ENHANCEMENT] Updated dependencies, including: #203
  * `github.com/prometheus/client_golang` from `v1.20.5` to `v1.21.1`
  * `github.com/prometheus/common` from `v0.62.0` to `v0.63.0`
  * `golang.org/x/sync` from `v0.11.0` to `v0.12.0`
  * `k8s.io/api` from `v0.32.1` to `v0.32.3`
  * `k8s.io/apimachinery` from `v0.32.1` to `v0.32.3`
  * `k8s.io/client-go` from `v0.32.1` to `v0.32.3`
  * `sigs.k8s.io/controller-runtime` from `v0.20.1` to `v0.20.4`

## v0.24.0

* [ENHANCEMENT] Update Go to `1.24` #196
* [ENHANCEMENT] Updated dependencies, including: #197
  * `github.com/prometheus/common` from `v0.61.0` to `v0.62.0`
  * `golang.org/x/sync` from `v0.10.0` to `v0.11.0`
  * `k8s.io/api` from `v0.32.0` to `v0.32.1`
  * `k8s.io/apimachinery` from `v0.32.0` to `v0.32.1`
  * `k8s.io/client-go` from `v0.32.0` to `v0.32.1`
  * `sigs.k8s.io/controller-runtime` from `v0.19.3` to `v0.20.1`

## v0.23.0

* [ENHANCEMENT] Make timeout for requests to Pods and to the Kubernetes control plane configurable. #188
* [ENHANCEMENT] Updated dependencies, including: #189 #191
  * `github.com/prometheus/client_golang` from `v1.20.4` to `v1.20.5`
  * `github.com/prometheus/common` from `v0.59.1` to `v0.61.0`
  * `k8s.io/api` from `v0.31.1` to `v0.32.0`
  * `k8s.io/apimachinery` from `v0.31.1` to `v0.32.0`
  * `k8s.io/client-go` from `v0.31.1` to `v0.32.0`
  * `sigs.k8s.io/controller-runtime` from `v0.19.0` to `v0.19.3`
  * `golang.org/x/net` from `v0.28.0` to `v0.33.0`

## v0.22.0

* [ENHANCEMENT] New parameter log.format allows to set logging format to logfmt (default) or json (new). #184
* [ENHANCEMENT] Add a 5 minute timeout to requests to Pods and to the Kubernetes control plane. #186
 
## v0.21.0

* [ENHANCEMENT] Log debug information about StatefulSets as they are created, updated and deleted. #182

## v0.20.1

* [BUGFIX] Improved handling of URL ports in `createPrepareDownscaleEndpoints` function. The function now correctly preserves the port when replacing the host in the URL. #176

## v0.20.0

* [ENHANCEMENT] Updated dependencies, including: #174
  * `github.com/prometheus/client_golang` from `v1.19.1` to `v1.20.4`
  * `github.com/prometheus/common` from `v0.55.0` to `v0.59.1`
  * `k8s.io/api` from `v0.30.3` to `v0.31.1`
  * `k8s.io/apimachinery` from `v0.30.3` to `v0.31.1`
  * `k8s.io/client-go` from `v0.30.3` to `v0.31.1`
  * `sigs.k8s.io/controller-runtime` from `v0.18.5` to `v0.19.0`

## v0.19.1

* [CHANGE] Renamed `grafana.com/rollout-mirror-replicas-from-resource-write-back-status-replicas` annotation to `grafana.com/rollout-mirror-replicas-from-resource-write-back`, because it was too long (over 64 chars). #171

## v0.19.0

* [ENHANCEMENT] Updated dependencies, including: #165
  * `github.com/prometheus/client_golang` from `v1.19.0` to `v1.19.1`
  * `github.com/prometheus/common` from `v0.53.0` to `v0.55.0`
  * `golang.org/x/sync` from `v0.7.0` to `v0.8.0`
  * `k8s.io/api` from `v0.30.0` to `v0.30.3`
  * `k8s.io/apimachinery` from `v0.30.0` to `v0.30.3`
  * `k8s.io/client-go` from `v0.30.0` to `v0.30.3`
  * `sigs.k8s.io/controller-runtime` from `v0.18.1` to `v0.18.5`
* [ENHANCEMENT] Update Go to `1.23`. #168
* [ENHANCEMENT] When mirroring replicas of statefulset, rollout-operator can now skip writing back number of replicas to reference resource, by setting `grafana.com/rollout-mirror-replicas-from-resource-write-back-status-replicas` annotation to `false`. #169

## v0.18.0

* [FEATURE] Optionally only scale-up a `StatefulSet` once all of the leader `StatefulSet` replicas are ready. Enable with `grafana.com/rollout-upscale-only-when-leader-ready` annotation set to `true`. #164

## v0.17.1

* [ENHANCEMENT] prepare-downscale admission webhook: undo prepare-shutdown calls if adding the `last-downscale` annotation fails. #151

## v0.17.0

* [CHANGE] The docker base images are now based off distroless images rather than Alpine. #149
  * The standard base image is now `gcr.io/distroless/static-debian12:nonroot`.
  * The boringcrypto base image is now `gcr.io/distroless/base-nossl-debian12:nonroot` (for glibc).
* [ENHANCEMENT] Include unique IDs of webhook requests in logs for easier debugging. #150
* [ENHANCEMENT] Include k8s operation username in request debug logs. #152
* [ENHANCEMENT] `rollout-max-unavailable` annotation can now be specified as percentage, e.g.: `rollout-max-unavailable: 25%`. Resulting value is computed as `floor(replicas * percentage)`, but is never less than 1. #153
* [ENHANCEMENT] Delayed downscale of statefulset can now reduce replicas earlier, if subset of pods at the end of statefulset have already reached their delay. #156
* [BUGFIX] Fix a mangled error log in controller's delayed downscale code. #154

## v0.16.0

* [ENHANCEMENT] If the POST to prepare-shutdown fails for any replica, attempt to undo the operation by issuing an HTTP DELETE to prepare-shutdown for all target replicas. #146

## v0.15.0

* [CHANGE] Rollout-operator is now released under an Apache License 2.0. #139, #140
* [ENHANCEMENT] Updated dependencies, including: #144
  * `github.com/prometheus/common` from `v0.49.0` to `v0.53.0`
  * `k8s.io/api` from `v0.29.2` to `v0.30.0`
  * `k8s.io/apimachinery` from `v0.29.2` to `v0.30.0`
  * `k8s.io/client-go` from `v0.29.2` to `v0.30.0`

## v0.14.0

* [FEATURE] Rollout-operator can now "mirror" replicas of statefulset from any reference resource. `status.replicas` field of reference resource is kept up-to-date with current number of replicas in target statefulset. #129
* [FEATURE] Rollout-operator can optionally delay downscale of statefulset when mirroring replicas from reference resource. Delay is coordinated with downscaled pods via new endpoint that pods must implement. #131
* [ENHANCEMENT] Update Go to `1.22`. #133
* [ENHANCEMENT] Updated dependencies, including: #137
  * `github.com/prometheus/client_golang` from `v1.18.0` to `v1.19.0`
  * `github.com/prometheus/common` from `v0.45.0` to `v0.49.0`
  * `k8s.io/api` from `v0.29.0` to `v0.29.2`
  * `k8s.io/apimachinery` from `v0.29.0` to `v0.29.2`
  * `k8s.io/client-go` from `v0.29.0` to `v0.29.2`

## v0.13.0

* [BUGFIX] Consider missing pods as not ready. #127

## v0.12.0

* [ENHANCEMENT] Add metrics for Kubernetes control plane calls. #118 #123

## v0.11.0

* [FEATURE] Coordinate downscaling between zones with a ConfigMap instead of annotation, optionally, via the new zoneTracker for the prepare-downscale admission webhook. #107
* [ENHANCEMENT] Expose pprof endpoint for profiling. #109
* [ENHANCEMENT] Change Docker build image to `golang:1.21-bookworm` and update base image to `alpine:3.19`. #97
* [ENHANCEMENT] Add basic tracing support. #101 #114 #115
* [ENHANCEMENT] Updated dependencies, including: #111
  * `github.com/gorilla/mux` from `v1.8.0` to `v1.8.1`
  * `github.com/prometheus/client_golang` from `v1.17.0` to `v1.18.0`
  * `golang.org/x/sync` from `v0.5.0` to `v0.6.0`
  * `k8s.io/api` from `v0.28.3` to `v0.29.0`
  * `k8s.io/apimachinery` from `v0.28.3` to `v0.29.0`
  * `k8s.io/client-go` from `v0.28.3` to `v0.29.0`

## v0.10.1

* [BUGFIX] Do not allow downscale if the operator failed to check whether there are StatefulSets with non-updated replicas. #105

## v0.10.0

* [ENHANCEMENT] Proceed with prepare-downscale operation even when pods from zone being downscaled are not ready or not up to date. #99
* [ENHANCEMENT] Expose metrics for incoming HTTP requests. #100

## v0.9.0

* [ENHANCEMENT] Updated dependencies, including: #93
  * `github.com/prometheus/client_golang` from `v1.16.0` to `v1.17.0`
  * `github.com/prometheus/common` from `v0.44.0` to `v0.45.0`
  * `golang.org/x/sync` from `v0.3.0` to `v0.4.0`
  * `k8s.io/api` from `v0.28.1` to `v0.28.3`
  * `k8s.io/apimachinery` from `v0.28.1` to `v0.28.3`
  * `k8s.io/client-go` from `v0.28.1` to `v0.28.3`

## v0.8.3

* [BUGFIX] Do not exit reconciliation early if there are errors while trying to adjust the number of replicas. #92

## v0.8.2

* [BUGFIX] Do not exit reconciliation early if there are errors while trying to adjust the number of replicas. #90

## v0.8.1

* [ENHANCEMENT] Update the prepare-downscale webhook to check that a rollout is not in progress in the rollout-group, else deny. #82

## v0.8.0

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
