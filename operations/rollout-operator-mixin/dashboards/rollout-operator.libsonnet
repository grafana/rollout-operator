function(cfg)

  local filename = if cfg.product == '' then std.asciiLower(cfg.rollout_operator_name + '.json') else std.asciiLower(cfg.product + '-' + cfg.rollout_operator_name + '.json');

  local utils = (import 'dashboard-utils.libsonnet') + { _config:: cfg };

  utils {

    local admissionWebhookRoutesMatcher = 'route=~"admission.*"',

    [filename]:
      assert cfg.rollout_operator_dashboard_uid == '' || std.md5(filename) == cfg.rollout_operator_dashboard_uid : 'UID of the dashboard has changed, please update references to dashboard. filename is now '+filename+'. Set rollout_operator_dashboard_uid=' + std.md5(filename) ;
      (utils.dashboard(cfg.rollout_operator_dashoard_title) + { uid: std.md5(filename) })
      .addClusterSelectorTemplates()
      .addRow(
        utils.row('Incoming webhook requests')
        .addPanel(
          utils.timeseriesPanel('Throughput by status') +
          utils.qpsPanel('rollout_operator_request_duration_seconds_count{%s, %s}' % [utils.jobMatcher(), admissionWebhookRoutesMatcher]) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Throughput by webhook') +
          utils.queryPanel(
            'sum by (route) (rate(rollout_operator_request_duration_seconds_count{%s, %s}[$__rate_interval]))' % [utils.jobMatcher(), admissionWebhookRoutesMatcher],
            '__auto',
          ) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Latency (all webhooks)') +
          utils.latencyPanel('rollout_operator_request_duration_seconds', '{%s, %s}' % [utils.jobMatcher(), admissionWebhookRoutesMatcher]) +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('p99 latency by webhook') +
          utils.queryPanel(
            'histogram_quantile(0.99, sum by (le, route) (rate(rollout_operator_request_duration_seconds_bucket{%s, %s}[$__rate_interval]))) * 1e3' % [utils.jobMatcher(), admissionWebhookRoutesMatcher],
            '__auto',
          ) +
          utils.units('ms') +
          utils.showAllSeriesInTooltip
        )
      )
      .addRow(
        utils.row('Reconciliation loop')
        .addPanel(
          local title = 'Reconciliation attempts by rollout group';

          utils.timeseriesPanel(title) +
          utils.panelDescription(title, 'This panel includes both successful and failed reconciliation attempts.') +
          utils.queryPanel(
            'sum by (namespace, rollout_group) (rate(rollout_operator_group_reconciles_total{%s}[$__rate_interval]))' % [utils.jobMatcher()],
            '{{namespace}}/{{rollout_group}}',
          ) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Reconciliation failures by rollout group') +
          utils.queryPanel(
            'sum by (namespace, rollout_group) (rate(rollout_operator_group_reconciles_failed_total{%s}[$__rate_interval]))' % [utils.jobMatcher()],
            '{{namespace}}/{{rollout_group}}',
          ) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Average reconcile duration by rollout group') +
          utils.queryPanel(
            '1e3 * sum by (namespace, rollout_group) (rate(rollout_operator_group_reconcile_duration_seconds_sum{%s}[$__rate_interval])) / sum by (namespace, rollout_group) (rate(rollout_operator_group_reconcile_duration_seconds_count{%s}[$__rate_interval]))' % [utils.jobMatcher(), utils.jobMatcher()],
            '{{namespace}}/{{rollout_group}}',
          ) +
          utils.units('ms') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Time since last successful reconcile') +
          utils.queryPanel(
            'time() - max by (namespace, rollout_group) (rollout_operator_last_successful_group_reconcile_timestamp_seconds{%s})' % [utils.jobMatcher()],
            '{{namespace}}/{{rollout_group}}',
          ) +
          utils.units('s') +
          utils.showAllSeriesInTooltip
        )
      )
      .addRow(
        utils.row('Outgoing Kubernetes control plane API requests')
        .addPanel(
          utils.timeseriesPanel('Throughput by status') +
          utils.qpsPanel('rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}' % utils.jobMatcher()) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Throughput by route') +
          utils.queryPanel(
            'sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval]))' % utils.jobMatcher(),
            '{{method}} {{path}}',
          ) +
          utils.units('reqps') +
          utils.showAllSeriesInTooltip
        )
        .addPanel(
          utils.timeseriesPanel('Average latency (by route)') +
          utils.queryPanel(
            [
              'sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_sum{%s}[$__rate_interval])) / sum by (method, path) (rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval])) * 1e3' % [utils.jobMatcher(), utils.jobMatcher()],
              'sum(rate(rollout_operator_kubernetes_api_client_request_duration_seconds_sum{%s}[$__rate_interval])) / sum(rate(rollout_operator_kubernetes_api_client_request_duration_seconds_count{%s}[$__rate_interval])) * 1e3' % [utils.jobMatcher(), utils.jobMatcher()],
            ],
            [
              '{{method}} {{path}}',
              'All routes',
            ]
          ) +
          {
            fieldConfig+: {
              overrides: [
                utils.overrideFieldByName('All routes', [
                  utils.overrideProperty('custom.lineStyle', { dash: [10, 10], fill: 'dash' }),
                  utils.overrideProperty('color', { mode: 'fixed', fixedColor: '#808080' }),
                ]),
              ],
            },
          } +
          utils.units('ms') +
          utils.showAllSeriesInTooltip
        )
      )
      .addRow(
        utils.row('Resources')
        .addPanel(
          utils.containerCPUUsagePanel,
        )
        .addPanel(
          utils.containerMemoryWorkingSetPanel,
        )
        .addPanel(
          utils.timeseriesPanel('Running instances') +
          utils.queryPanel(
            'sum(up{%s})' % [utils.jobMatcher()],
            'Instances'
          ) +
          utils.units('instance') +
          utils.min(0) +
          utils.hideLegend
        )
      ),
  }
