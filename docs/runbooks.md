# Rollout-operator runbooks

## Alerts

### IncorrectWebhookConfigurationFailurePolicy

This alert fires when one of the validating webhooks used by the rollout-operator has its failure policy set to `Ignore`.

In normal operations the failure policy should be set to `Fail`.

```
kind: ValidatingWebhookConfiguration
...
  failurePolicy: Fail
```

How it **works**:

- This alert checks on the configured failure policy of the `pod-eviction` and `zpdb-validation` validating webhooks via the `kube_validating_webhook_failure_policy` metric
- Although it may be valid to temporarily enable an `Ignore` failure mode, normal operations should have the failure mode set to `Fail`
- When in `Ignore` mode the Kubernetes API server ignores a webhook failure if the webhook is unavailable or can not be reached
- This would result in the zone aware pod disruption budget not being enforced or the ZPDB configuration validator not being enforced if the rollout-operator is not running

How to **investigate**:

- Review the configuration of the `pod-eviction` and `zpdb-configuration` webhooks:
  - `kubectl -n <namespace> get ValidatingWebhookConfigurations zpdb-validation-<namespace> -o yaml`
  - `kubectl -n <namespace> get ValidatingWebhookConfigurations pod-eviction-<namespace> -o yaml`
- Update the configuration to use a `Fail` policy. See Jsonnet configuration options `ignore_rollout_operator_zpdb_eviction_webhook_failures` and `ignore_rollout_operator_zpdb_validation_webhook_failures` in [rollout-operator.libsonnet](https://github.com/grafana/rollout-operator/blob/main/operations/rollout-operator/rollout-operator.libsonnet).

### BadZoneAwarePodDisruptionBudgetConfiguration

This alert fires when the zone aware pod disruption budget configuration validating webhook observes an invalid ZPDB object.

This indicates that a malformed configuration has been applied, or an invalid configuration already exists when the rollout-operator starts.

How it **works**:

- Under normal circumstances, there is a `zpdb-validation` validating webhook that should prevent ZPDBs with invalid configuration from being accepted by the Kubernetes control plane
- However, if the validating webhook did not reject the invalid ZPDB for some reason, then the rollout-operator will reject and ignore the invalid ZPDB, so the ZPDB will not be effective, and this alert will fire
- The invalid ZPDB could have been accepted for a number of reasons, including:
  - The validating webhook was not installed correctly when the invalid ZPDB was stored
  - The validating webhook's failure policy was set to `Ignore` instead of `Fail`

How to **investigate**:

- Review the `zpdb-validation` `ValidatingWebhookConfiguration` and ensure its failure policy is set to `Fail`:
  `kubectl -n <namespace> get ValidatingWebhookConfigurations zpdb-validation-<namespace> -o yaml`
- Review the `ZPDB` configurations and verify they are valid
  `kubectl -n <namespace> get zpdb <name> -o yaml`

### HighNumberInflightZpdbRequests

This alert fires when there has been a sustained number of in-flight pod eviction requests for a given period of time.

This indicates that there is likely an issue causing a delay in the pod eviction consideration process.

How it **works**:

- The `pod-eviction` `ValidatingWebhookConfiguration` routes voluntary pod eviction requests into the ZPDB eviction controller
- The rollout controller also uses the ZPDB eviction controller to test if a pod can be updated (as part of rolling updates)
- The `ZPDB` eviction controller serializes these requests, such that only one pod is considered at a time. Other requests are queued
- The `ZPDB` eviction controller relies on the Kubernetes API to query for status on StatefulSets and Pods

How to **investigate**:

- Review the rollout-operator logs (or trace) to gain insight into what may be causing a delay or blockage in the `ZPDB` eviction controller
- Use caution with restarting the rollout-operator pod. It maintains internal state of recently evicted pods
  - There is a short window of time from when an eviction request is allowed to the pod transitioning to a state where it will report as not ready
  - The rollout-operator maintains an in-memory cache of these recently evicted pods to compensate for the pod still reporting as ready after its eviction request has been allowed
  - In normal circumstances with the pod eviction webhook failure policy set to `Fail`, by the time a rollout-operator pod has been restarted the recently evicted pod states will be correctly reconciled
  - But if pods have been evicted with a failure policy of `Ignore` then there is a small possibility for a race condition which can result in a ZPDB breach
- Ensure that the `pod-eviction` and `zpdb-validation` `ValidatingWebhookConfiguration` have a failure policy set to `Fail` before restarting the rollout-operator

### rollout-operatorKubernetesAPIClientRateLimited

This alert fires when the rollout-operator has been unable to send Kubernetes API requests because its own client-side rate limiter is exhausted.

There is no healthy non-zero level for this: a request which fails to acquire a token is never sent at all, and the caller sees it as a failure. A sustained rate means the rate limiter, rather than the Kubernetes API, is the bottleneck.

How it **works**:

- The rollout-operator rate limits its calls to the Kubernetes API with a separate token bucket per API group, defaulting to `5` QPS and a burst of `10` per group
- `rollout_operator_kubernetes_api_client_rate_limited_requests_total` counts requests which waited for a token until they exceeded their context deadline, labelled by `api_group`
- The alert fires on any sustained rate, held for 15 minutes so that a momentary burst does not page
- The effect is self-sustaining: eviction requests which time out are retried, which deepens the queue, so it does not usually recover on its own until the eviction rate drops

How to **investigate**:

- Identify the saturated API group from the `api_group` label. `core` and `apps` are the ones the eviction path uses
- Review the rollout-operator error logs. Note that once the queue is deep enough, requests stop reaching the limiter's rejection path and time out without the `client-side rate limiter` marker, so searching for that string alone under-reports the problem. See [Recognising exhaustion](#recognising-exhaustion) for the log forms to expect
- Check `rollout_operator_zpdb_inflight_eviction_requests`. It settles near `QPS × webhook deadline`, so a value stuck around 45 with the default limits indicates a fully saturated bucket
- Establish what is driving the eviction volume. Rollout-operator rolls pods out sequentially, so a rolling update is not a source of concurrency here; the usual cause is a large-scale node drain - node pressure, or a cluster autoscaler consolidating - evicting many pods across many zones at once
- To mitigate, either reduce the eviction concurrency at its source, or raise the limits. See [Kubernetes API client rate limiting](#kubernetes-api-client-rate-limiting)

### rollout-operatorKubernetesAPIClientApproachingRateLimit

This alert fires when a rollout-operator pod is sustaining over 80% of its own configured client-side rate limit for a given Kubernetes API group. It is an earlier, lower-urgency warning than [rollout-operatorKubernetesAPIClientRateLimited](#rollout-operatorkubernetesapiclientratelimited): nothing is being dropped yet, but the pod is close enough to its ceiling that a small increase in load would start rejecting requests.

How it **works**:

- The token bucket is per-process, so the alert compares each pod's own throughput against its own limit rather than a fleet-wide sum
- `rollout_operator_kubernetes_api_client_rate_limit_qps` publishes the configured `-kubernetes.client-qps` value, but only while rate limiting is actually enabled (`qps` and `burst` both positive). Where it is disabled, the metric is absent and this alert cannot fire for that pod - there is no ceiling to approach
- The alert fires on throughput exceeding 80% of that limit, held for 10 minutes so a momentary burst does not page

How to **investigate**:

- Identify the pod and API group approaching its limit from the `pod` and `api_group` labels
- Establish what is driving the load the same way as for the saturation alert: usually a large-scale node drain rather than a rolling update, since rollout-operator rolls pods out sequentially
- Since this fires before anything is actually being dropped, there is more room to act before impact: reduce the load at its source, or raise the limits. See [Kubernetes API client rate limiting](#kubernetes-api-client-rate-limiting)
- If it keeps firing without ever tipping into the saturation alert, the limit may simply be sized close to normal peak load - consider raising it rather than treating every firing as an incident

## Metrics

A Prometheus metrics endpoint is available at `/metrics` of the rollout-operator deployment.

### kube_validating_webhook_failure_policy

This metric reports on the current configuration of validating and mutating webhook failure policy configurations.

Labels are used to indicate the policy mode (`Fail` or `Ignore`) and the webhook details. A value of `1` indicates that this is the current setting.

Use this metric to monitor that the webhooks have been correctly configured.

### rollout_operator_zpdb_configurations_observed_total

This counter metric reports on the total number of `ValidatingWebhookConfiguration` configurations which have been `updated` (including additions) or `deleted`.

It also tracks the number of `ignored` and `invalid` configuration updates.

A configuration will be `ignored` if it is a stale update or an update in an unexpected format.

A configuration will be `invalid` if it fails a validation process.

Use this metric to monitor for changes to the `ZPDB` configurations and to monitor for unexpected `invalid` configurations being observed. This may indicate an error in the generation of these configurations.

### rollout_operator_zpdb_eviction_requests_total

This counter metric reports on the total number of pod eviction requests.

This includes both pod eviction requests which come in via the pod eviction webhook, and pod deletion requests which come from the rollout controller (StatefulSet pod updates).

Note that the number of requests arriving via the pod eviction webhook can be tracked with the `rollout_operator_kubernetes_api_client_request_duration_seconds_count` metric.

Use this metric to monitor for abnormal request volume and/or frequency.

### rollout_operator_zpdb_inflight_eviction_requests

This is a gauge metric which tracks the number of in-flight pod eviction requests.

Like `rollout_operator_zpdb_eviction_requests_total`, this metrics takes both eviction requests which come in via the pod eviction webhook, and pod deletion requests which come from the rollout controller (StatefulSet pod updates).

Note that the number of in-flight requests via the pod eviction webhook can be tracked with the `rollout_operator_inflight_requests` metric.

Use this metric to monitor for abnormal high volumes of in-flight requests. Since these eviction requests should return quickly, even a small number of sustained in-flight requests is likely indicative of an issue.

Check that the rollout-operator error logs to gain insight into why the eviction is being delayed.

### kube_customresource_zpdb_spec_max_unavailable

This is a gauge metric which tracks the configured max unavailable setting for each rollout-group. For instance `kube_customresource_zpdb_spec_max_unavailable{name="ingester-rollout"}`.

Use this metric to track that your `ZPDB` configurations are correctly set to the expected value.

## Configuration

See [rollout-operator.libsonnet](https://github.com/grafana/rollout-operator/blob/main/operations/rollout-operator/rollout-operator.libsonnet).

### Webhook failure policies

The following Jsonnet flags can be set to toggle the webhook failure modes. These should be used with caution.

Setting these to `true` will result in the Kubernetes API server proceeding if the webhook is not reachable / rollout-operator pod is not running.

```jsonnet
_config+:: {
    ignore_rollout_operator_no_downscale_webhook_failures: true|false,
    ignore_rollout_operator_prepare_downscale_webhook_failures: true|false,
    ignore_rollout_operator_zpdb_validation_webhook_failures: true|false,
    ignore_rollout_operator_zpdb_eviction_webhook_failures: true|false
```

Note that if you are using the rollout-operator [Helm chart](https://github.com/grafana/helm-charts/tree/main/charts/rollout-operator) there are equivalent [values](https://github.com/grafana/helm-charts/blob/main/charts/rollout-operator/values.yaml) for changing the webhook failure policies.

### Kubernetes API client rate limiting

The rollout-operator rate limits its own calls to the Kubernetes API, with a separate token bucket per API
group (`core`, `apps`, and so on). The defaults are `5` QPS and a burst of `10` per group.

Each admission webhook and the core controller get their own client, so an overloaded webhook exhausts only
its own buckets. Within one webhook, however, the bucket is shared by every request it is serving.

The default values can be overridden such as;

```jsonnet
rollout_operator_args+:: {
    'kubernetes.client-qps': 20,
    'kubernetes.client-burst': 40,
}
```

Increasing these values will place additional load onto the Kubernetes API server.

The rate limits can be disabled with;

```jsonnet
rollout_operator_args+:: {
    'kubernetes.client-qps': 0,
}
```

#### Recognising exhaustion

When a bucket is exhausted, requests queue for a token until they would exceed their context deadline, at
which point they are rejected without being sent. The rejection surfaces wrapped in whatever the caller was
trying to do, so the same root cause appears under several different `reason` values.

Pod `Get` for the pod being evicted, on the `core` group:

```
level=error method=admission.PodEviction() object.name=store-gateway-zone-b-243 object.namespace=<namespace>
  msg="pod eviction denied" reason="unable to find pod by name"
  err="Get \"https://<apiserver>/api/v1/namespaces/<namespace>/pods/store-gateway-zone-b-243?timeout=5m0s\":
  client-side rate limiter for Kubernetes API group \"core\": rate: Wait(n=1) would exceed context deadline"
```

StatefulSet `List` to find the other zones in the rollout group, on the `apps` group:

```
level=error method=admission.PodEviction() object.name=store-gateway-zone-b-1 object.namespace=<namespace>
  owner=store-gateway-zone-b msg="pod eviction denied"
  reason="unable to find related stateful sets - a minimum of 2 StatefulSets is required"
  err="Get \"https://<apiserver>/apis/apps/v1/namespaces/<namespace>/statefulsets?labelSelector=poddisruptionbudget-group%3Dnon-spot-store-gateway\":
  client-side rate limiter for Kubernetes API group \"apps\": rate: Wait(n=1) would exceed context deadline"
```

Once the pile-up is deep enough, requests stop reaching the limiter's rejection path and simply run out of
time, losing the `client-side rate limiter` marker altogether:

```
level=error method=admission.PodEviction() object.name=ingester-zone-b-503 object.namespace=<namespace>
  msg="pod eviction denied" reason="unable to find pod owner"
  err="unable to find StatefulSet ingester-zone-b by name:
  Get \"https://<apiserver>/apis/apps/v1/namespaces/<namespace>/statefulsets/ingester-zone-b?timeout=5m0s\": context deadline exceeded"
```

The last form is the one to watch for, because searching only for `client-side rate limiter` will
under-report how bad things are.

Confirm with the metrics rather than the logs alone:

- `rollout_operator_kubernetes_api_client_rate_limited_requests_total` counts requests which failed to
  acquire a token, by `api_group`. Any sustained rate here means the limiter is the bottleneck.
- `rollout_operator_kubernetes_api_client_request_duration_seconds` counts only the requests which did get a
  token. If its rate is pinned flat at the configured QPS, the bucket is saturated.
- `rollout_operator_zpdb_inflight_eviction_requests` will sit at a sustained non-zero level as evictions
  queue. It settles near `QPS × webhook deadline`, so a value stuck around 45 with the defaults is a
  fully saturated `core` bucket.

The effect is self-sustaining: evictions that time out are retried, which deepens the queue. It does not
recover on its own until the eviction rate drops.

### Disable voluntary pod evictions

This example illustrates using a `mimir` identifier.

```jsonnet
ingester_rollout_pdb+:
  local podDisruptionBudget = $.policy.v1.podDisruptionBudget;
  podDisruptionBudget.mixin.spec.withMaxUnavailable(0),
```

### Cross-zone eviction delays

The `crossZoneEvictionDelay` can be used in partition-aware `ZPDB` configurations and requires `podNamePartitionRegex` to be set.

It requires a minimum period to elapse after a pod returns to ready+running before another pod in the same partition (in any zone) can be evicted.

For instance;

- t0 - `pod-zone-a-0` is evicted
- t1 - `pod-zone-a-0` returns to full service (ready and running)
- t2 - `pod-zone-b-0` is tested for eviction

Assuming the PDB `max-unavailable` is 1, the `pod-zone-b-0` eviction will not be allowed until at least `crossZoneEvictionDelay` has elapsed since t1 (i.e. `t2 - t1 >= crossZoneEvictionDelay`).

Note that this duration is calculated from when the `pod-zone-a-0` becomes ready, not when it was evicted.

When `crossZoneEvictionDelay` is unset or `0`, no delay is enforced and evictions follow the standard ZPDB logic.

The time that a pod became ready is recorded by setting a `grafana.com/ready-time` annotation on the pod. This ensures that
if the `rollout-operator` restarts the last ready time is not lost. The annotation is automatically removed when a pod transitions
out of a ready+running state, so the delay is correctly re-measured from the next ready transition. The annotation is also lost if a pod is re-created.

When the annotation is missing - e.g. the first time this version of the `rollout-operator` runs, or after a pod is re-created during
a `rollout-operator` restart - the `rollout-operator` records the current time as the ready time on its first observation of the pod. The
delay window therefore runs from the `rollout-operator`'s first observation, not from the pod's actual ready time. Pod evictions
(and rolling updates) within the same partition are denied until that window expires.

This can be monitored in the `rollout-operator` logs via the message `Pod not considered ready - not enough time has elapsed since this pod became ready`.
