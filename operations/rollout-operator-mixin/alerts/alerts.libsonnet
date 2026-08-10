local utils = import 'mixin-utils/utils.libsonnet';

(import 'alerts-utils.libsonnet') {

  local alertGroups = [
    {
      name: 'rollout_operator_alerts',
      rules: [
        {
          alert: $.alertName('IncorrectWebhookConfigurationFailurePolicy'),
          expr: 'count by(type, webhook, namespace) (kube_validating_webhook_failure_policy{policy="Ignore", webhook=~"^(pod-eviction|zpdb-validation).+"} > 0)',
          'for': '5m',
          labels: {
            severity: 'warning',
          },
          annotations: {
            message: |||
              A validating or mutating rollout-operator webhook has an Ignore policy set. This should be set to Fail.
            |||,
          },
        },
        {
          alert: $.alertName('BadZoneAwarePodDisruptionBudgetConfiguration'),
          expr: 'sum by (job, namespace)(rate(rollout_operator_zpdb_configurations_observed_total{result="invalid"}[5m])) > 0',
          'for': '5m',
          labels: {
            severity: 'warning',
          },
          annotations: {
            message: 'An invalid zone aware pod disruption budget configuration has been observed.',
          },
        },
        {
          alert: $.alertName('HighNumberInflightZpdbRequests'),
          expr: 'avg_over_time(rollout_operator_zpdb_inflight_eviction_requests[5m]) > 10',
          'for': '5m',
          labels: {
            severity: 'warning',
          },
          annotations: {
            message: 'A sustained number of inflight ZPDB eviction requests has been observed.',
          },
        },
        {
          // There is no healthy non-zero level for this counter: a request which fails to acquire a token is
          // never sent. Any sustained rate means the client-side rate limiter has become the bottleneck, so
          // this alerts on > 0 held for long enough to rule out a momentary burst.
          alert: $.alertName('KubernetesAPIClientRateLimited'),
          expr: 'sum by (namespace, api_group) (rate(rollout_operator_kubernetes_api_client_rate_limited_requests_total[5m])) > 0',
          'for': '15m',
          labels: {
            severity: 'warning',
          },
          annotations: {
            message: 'The rollout-operator is dropping Kubernetes API requests against the {{ $labels.api_group }} API group because its client-side rate limiter is exhausted.',
          },
        },
      ],
    },
  ],
  groups+: $.withRunbookURL('https://github.com/grafana/rollout-operator/tree/main/docs/runbooks.md#%s', $.withExtraLabelsAnnotations(alertGroups)),
}
