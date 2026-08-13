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
        {
          // Compares each pod's own throughput against its own configured limit, since the token bucket is
          // per-process. component is also kept in the grouping: each admission webhook and the core
          // controller get their own dedicated client with independent buckets at the same nominal QPS, so
          // summing across them would let a genuinely idle client hide one running hot, or several
          // individually-fine clients look saturated together. rollout_operator_kubernetes_api_client_rate_limit_qps
          // is only published while rate limiting is enabled, so this alert is naturally absent - not a
          // divide-by-zero - where it isn't.
          alert: $.alertName('KubernetesAPIClientApproachingRateLimit'),
          expr: |||
            sum by (%(instance_labels)s, component, api_group) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count[5m]))
            / on (%(instance_labels)s, component) group_left()
            max by (%(instance_labels)s, component) (rollout_operator_kubernetes_api_client_rate_limit_qps)
            > 0.8
          ||| % { instance_labels: 'namespace, pod' },
          'for': '10m',
          labels: {
            severity: 'warning',
          },
          annotations: {
            message: 'The rollout-operator pod {{ $labels.pod }}' + "'" + 's {{ $labels.component }} client is sustaining over 80% of its configured client-side rate limit for the {{ $labels.api_group }} Kubernetes API group. It is not yet dropping requests, but is at risk of doing so.',
          },
        },
      ],
    },
  ],
  groups+: $.withRunbookURL('https://github.com/grafana/rollout-operator/tree/main/docs/runbooks.md#%s', $.withExtraLabelsAnnotations(alertGroups)),
}
