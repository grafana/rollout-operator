local utils = import 'mixin-utils/utils.libsonnet';

(import 'alerts-utils.libsonnet') {

  local alertGroups = [
    {
      name: 'rollout_operator_alerts',
      rules: [
        {
          alert: $.alertName('IncorrectWebhookConfigurationFailurePolicy'),
          expr: |||
            count by(type, webhook) (kube_validating_webhook_failure_policy{policy="Ignore", webhook=~"^(pod-eviction|zpdb-validation)%s"} > 0)
          ||| % $._config.webhook_domain(),
          'for': '1h',
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
          expr: 'sum by (job)(rate(rollout_operator_zpdb_configurations_observed_total{result="invalid"}[5m])) > 0',
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
      ],
    },
  ],
  groups+: $.withRunbookURL('https://github.com/grafana/rollout-operator/tree/main/docs/runbooks.md#%s', $.withExtraLabelsAnnotations(alertGroups)),
}
