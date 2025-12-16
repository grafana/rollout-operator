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

- Review the `zpdb-validation` `ValidatingWebhookConfiguration` and ensure its failure policy is set to `Fail`
- `kubectl -n <namespace> get ValidatingWebhookConfigurations zpdb-validation-<namespace> -o yaml`
- Review the `ZPDB` configurations and verify they are valid
- `kubectl -n <namespace> get zpdb <name> -o yaml`

### HighNumberInflightZpdbRequests

This alert fires when there has been a sustained number of in-flight pod eviction requests for a given period of time.

This indicates that there is likely an issue causing a delay in the pod eviction consideration process.

How it **works**:

- The `pod-eviction` `ValidatingWebhookConfiguration` routes voluntary pod eviction requests into the ZPDB eviction controller
- The rollout controller uses the ZPDB eviction controller to test if a pod can be updated (as part of rolling updates)
- The `ZPDB` eviction controller serializes these requests, such that only one pod is considered at a time. Other requests are queued
- The `ZPDB` eviction controller relies on the Kubernetes API to query for status on StatefulSets and Pods

How to **investigate**:

- Review the rollout-operator logs (or trace) to gain insight into what may be causing a delay or blockage in the `ZPDB` eviction controller
- Use caution with restarting the rollout-operator pod. It maintains internal state of recently evicted pods
  - There is a short window of time from when an eviction request is allowed to the pod transitioning to a state where it will report as not ready
  - The rollout-operator maintains an in-memory cache of these recently evicted pods to compensate for the pod still reporting as ready after it's eviction request has been allowed
  - In normal circumstances with the pod eviction webhook failure policy set to `Fail`, by the time a rollout-operator pod has been restarted the recently evicted pod states will be correctly reconciled
  - But if pods have been evicted with a failure policy of `Ignore` then there is a small possibility for a race condition which can result in a ZPDB breach 
- Ensure that the `pod-eviction` and `zpdb-validation` `ValidatingWebhookConfiguration` have a failure policy set to `Fail` before restarting the rollout-operator

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

## Configuration

See [rollout-operator.libsonnet](https://github.com/grafana/rollout-operator/blob/main/operations/rollout-operator/rollout-operator.libsonnet).

### Webhook failure policies

The following Jsonnet flags can be set to toggle the webhook failure modes. These should be used with caution.

Setting these to `true` will result in the Kubernetes API server proceeding if the webhook is not reachable / rollout-operator pod is not running.

```Jsonnet
_config+:: {
    ignore_rollout_operator_no_downscale_webhook_failures: true|false,
    ignore_rollout_operator_prepare_downscale_webhook_failures: true|false,
    ignore_rollout_operator_zpdb_validation_webhook_failures: true|false,
    ignore_rollout_operator_zpdb_eviction_webhook_failures: true|false
```

Note that if you are using the rollout-operator [Helm chart](https://github.com/grafana/helm-charts/tree/main/charts/rollout-operator) there is a single configuration value of `webhooks.failurePolicy` which can be set to `Fail` or `Ignore` and this is applied to all the webhooks.

### Disable voluntary pod evictions

This example illustrates using a `mimir` identifier.

```Jsonnet
ingester_rollout_pdb+:
  local podDisruptionBudget = $.policy.v1.podDisruptionBudget;
  podDisruptionBudget.mixin.spec.withMaxUnavailable(0),
```
