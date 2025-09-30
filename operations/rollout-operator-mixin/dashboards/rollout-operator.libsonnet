local filename = 'rollout-operator.json';
(import 'dashboard-utils.libsonnet') {
  local admissionWebhookRoutesMatcher = 'route=~"admission.*"',

  [filename]:
    assert $._config.rollout_operator_dashboard_uid == '' || std.md5(filename) == $._config.rollout_operator_dashboard_uid : 'UID of the dashboard has changed, please update references to dashboard. filename is now ' + filename + '. Set rollout_operator_dashboard_uid=' + std.md5(filename);
    ($.rolloutOperator_dashboard($._config.rollout_operator_dashboard_title) + { uid: std.md5(filename) })
    .addClusterSelectorTemplates()
    .addRow(
      $.row('Incoming webhook requests')
      .addPanel(
        $.timeseriesPanel('Throughput by status') +
        $.qpsPanel('rollout_operator_request_duration_seconds_count{%s, %s}' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher]) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Throughput by webhook') +
        $.queryPanel(
          'sum by (route) (rate(rollout_operator_request_duration_seconds_count{%s, %s}[$__rate_interval]))' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher],
          '__auto',
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Latency (all webhooks)') +
        $.latencyPanel('rollout_operator_request_duration_seconds', '{%s, %s}' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher]) +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('p99 latency by webhook') +
        $.queryPanel(
          'histogram_quantile(0.99, sum by (le, route) (rate(rollout_operator_request_duration_seconds_bucket{%s, %s}[$__rate_interval]))) * 1e3' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher],
          '__auto',
        ) +
        $.units('ms') +
        $.showAllSeriesInTooltip
      )
    )
    .addRow(
      $.row('Reconciliation loop')
      .addPanel(
        local title = 'Reconciliation attempts by rollout group';

        $.timeseriesPanel(title) +
        $.panelDescription(title, 'This panel includes both successful and failed reconciliation attempts.') +
        $.queryPanel(
          'sum by (namespace, rollout_group) (rate(rollout_operator_group_reconciles_total{%s}[$__rate_interval]))' % [$.rolloutOperator_jobMatcher()],
          '{{namespace}}/{{rollout_group}}',
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Reconciliation failures by rollout group') +
        $.queryPanel(
          'sum by (namespace, rollout_group) (rate(rollout_operator_group_reconciles_failed_total{%s}[$__rate_interval]))' % [$.rolloutOperator_jobMatcher()],
          '{{namespace}}/{{rollout_group}}',
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Average reconcile duration by rollout group') +
        $.queryPanel(
          '1e3 * sum by (namespace, rollout_group) (rate(rollout_operator_group_reconcile_duration_seconds_sum{%s}[$__rate_interval])) / sum by (namespace, rollout_group) (rate(rollout_operator_group_reconcile_duration_seconds_count{%s}[$__rate_interval]))' % [$.rolloutOperator_jobMatcher(), $.rolloutOperator_jobMatcher()],
          '{{namespace}}/{{rollout_group}}',
        ) +
        $.units('ms') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Time since last successful reconcile') +
        $.queryPanel(
          'time() - max by (namespace, rollout_group) (rollout_operator_last_successful_group_reconcile_timestamp_seconds{%s})' % [$.rolloutOperator_jobMatcher()],
          '{{namespace}}/{{rollout_group}}',
        ) +
        $.units('s') +
        $.showAllSeriesInTooltip
      )
    )
    .addRow(
      $.row('Outgoing Kubernetes control plane API requests')
      .addPanel(
        $.timeseriesPanel('Throughput by status') +
        $.qpsPanel('rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}' % $.rolloutOperator_jobMatcher()) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Throughput by route') +
        $.queryPanel(
          'sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval]))' % $.rolloutOperator_jobMatcher(),
          '{{method}} {{path}}',
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Average latency (by route)') +
        $.queryPanel(
          [
            'sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_sum{%s}[$__rate_interval])) / sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval])) * 1e3' % [$.rolloutOperator_jobMatcher(), $.rolloutOperator_jobMatcher()],
            'sum(rate(rollout_operator_kubernetes_api_client_request_duration_seconds_sum{%s}[$__rate_interval])) / sum(rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval])) * 1e3' % [$.rolloutOperator_jobMatcher(), $.rolloutOperator_jobMatcher()],
          ],
          [
            '{{method}} {{path}}',
            'All routes',
          ]
        ) +
        {
          fieldConfig+: {
            overrides: [
              $.overrideFieldByName('All routes', [
                $.overrideProperty('custom.lineStyle', { dash: [10, 10], fill: 'dash' }),
                $.overrideProperty('color', { mode: 'fixed', fixedColor: '#808080' }),
              ]),
            ],
          },
        } +
        $.units('ms') +
        $.showAllSeriesInTooltip
      )
    )
    .addRow(
      $.row('Resources')
      .addPanel(
        $.rolloutOperator_containerCPUUsagePanel,
      )
      .addPanel(
        $.rolloutOperator_containerMemoryWorkingSetPanel,
      )
      .addPanel(
        $.timeseriesPanel('Running instances') +
        $.queryPanel(
          'sum(up{%s})' % [$.rolloutOperator_jobMatcher()],
          'Instances'
        ) +
        $.units('instance') +
        $.min(0) +
        $.hideLegend
      )
    ),
}
