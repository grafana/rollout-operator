local utils = import 'mixin-utils/utils.libsonnet';

local filename = 'rollout-operator.json';
(import 'dashboard-utils.libsonnet') {
  local admissionWebhookRoutesMatcher = 'route=~"admission.*"',

  local metrics = {
    request_duration_seconds: 'rollout_operator_request_duration_seconds',
    reconcile_duration_seconds: 'rollout_operator_group_reconcile_duration_seconds',
    k8s_api_client_request_duration_seconds: 'rollout_operator_kubernetes_api_client_request_duration_seconds',
  },

  [filename]:
    assert $._config.rollout_operator_dashboard_uid == '' || std.md5(filename) == $._config.rollout_operator_dashboard_uid : 'UID of the dashboard has changed, please update references to dashboard. filename is now ' + filename + '. Set rollout_operator_dashboard_uid=' + std.md5(filename);
    ($.rolloutOperator_dashboard($._config.rollout_operator_dashboard_title) + { uid: std.md5(filename) })
    .addClusterSelectorTemplates()
    .addRow(
      $.row('Incoming webhook requests')
      .addPanel(
        $.timeseriesPanel('Throughput by status') +
        $.qpsPanelNativeHistogram(metrics.request_duration_seconds, '%s, %s' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher]) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Throughput by webhook') +
        $.queryPanel(
          local selector = '%s, %s' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher];
          local query = utils.ncHistogramSumBy(
            query=utils.ncHistogramCountRate(metrics.request_duration_seconds, selector),
            sum_by=['route'],
          );
          [utils.showClassicHistogramQuery(query), utils.showNativeHistogramQuery(query)],
          ['__auto', '__auto'],
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Latency (all webhooks)') +
        $.ncLatencyPanel(metrics.request_duration_seconds, '%s, %s' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher]) +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('p99 latency by webhook') +
        $.queryPanel(
          local selector = '%s, %s' % [$.rolloutOperator_jobMatcher(), admissionWebhookRoutesMatcher];
          local query = utils.ncHistogramQuantile('0.99', metrics.request_duration_seconds, selector, sum_by=['route'], multiplier='1e3');
          [utils.showClassicHistogramQuery(query), utils.showNativeHistogramQuery(query)],
          ['__auto', '__auto'],
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
          local selector = '%s' % [$.rolloutOperator_jobMatcher()];
          local query = utils.ncHistogramAverageRate(metrics.reconcile_duration_seconds, selector, sum_by=['namespace', 'rollout_group'], multiplier='1e3');
          [utils.showClassicHistogramQuery(query), utils.showNativeHistogramQuery(query)],
          ['{{namespace}}/{{rollout_group}}', '{{namespace}}/{{rollout_group}}'],
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
        $.qpsPanelNativeHistogram(metrics.k8s_api_client_request_duration_seconds, $.rolloutOperator_jobMatcher()) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Throughput by route') +
        $.queryPanel(
          local selector = '%s' % [$.rolloutOperator_jobMatcher()];
          local query = utils.ncHistogramSumBy(
            query=utils.ncHistogramCountRate(metrics.k8s_api_client_request_duration_seconds, selector),
            sum_by=['method', 'path'],
          );
          [utils.showClassicHistogramQuery(query), utils.showNativeHistogramQuery(query)],
          ['{{method}} {{path}}', '{{method}} {{path}}'],
        ) +
        $.units('reqps') +
        $.showAllSeriesInTooltip
      )
      .addPanel(
        $.timeseriesPanel('Average latency (by route)') +
        $.queryPanel(
          local selector = $.rolloutOperator_jobMatcher();
          local byPathQuery = utils.ncHistogramAverageRate(metrics.k8s_api_client_request_duration_seconds, selector, multiplier='1e3', sum_by=['method', 'path']);
          local allRoutesQuery = utils.ncHistogramAverageRate(metrics.k8s_api_client_request_duration_seconds, selector, multiplier='1e3');
          [
            utils.showClassicHistogramQuery(byPathQuery),
            utils.showNativeHistogramQuery(byPathQuery),
            utils.showClassicHistogramQuery(allRoutesQuery),
            utils.showNativeHistogramQuery(allRoutesQuery),
          ],
          [
            '{{method}} {{path}}',
            '{{method}} {{path}}',
            'All routes',
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
