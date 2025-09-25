local utils = import 'mixin-utils/utils.libsonnet';

// This is a modified version of the mimir-mixin/dashboard-utils.libsonnet

local builder = import 'grafana-builder/grafana.libsonnet';

builder {
  _colors:: {
    resourceRequest: '#FFC000',
    resourceLimit: '#E02F44',
    success: '#7EB26D',
    warning: '#EAB839',
    failed: '#E24D42',  // "error" is reserved word in Jsonnet.
  },

  local resourceRequestStyle = $.overrideFieldByName('request', [
    $.overrideProperty('color', { mode: 'fixed', fixedColor: $._colors.resourceRequest }),
    $.overrideProperty('custom.fillOpacity', 0),
    $.overrideProperty('custom.lineStyle', { fill: 'dash' }),
  ]),

  local resourceLimitStyle = $.overrideFieldByName('limit', [
    $.overrideProperty('color', { mode: 'fixed', fixedColor: $._colors.resourceLimit }),
    $.overrideProperty('custom.fillOpacity', 0),
    $.overrideProperty('custom.lineStyle', { fill: 'dash' }),
  ]),

  local sortAscending = 1,

  row(title)::
    super.row(title),

  dashboard(title)::
    super.dashboard(
      title=if std.get($._config, 'dashboard_prefix') == null then title else '%(prefix)s%(title)s' % { prefix: $._config.dashboard_prefix, title: title },
      datasource=$._config.dashboard_datasource,
      datasource_regex=$._config.datasource_regex
    ) + {
      graphTooltip: $._config.graph_tooltip,
      __requires: [
        {
          id: 'grafana',
          name: 'Grafana',
          type: 'grafana',
          version: '8.0.0',
        },
      ],

      refresh: '5m',

      addClusterSelectorTemplates()::
        local d = self {
          tags: $._config.tags,
          links: $._config.rollout_operator_dashboard_links,
        };

        d
        .addMultiTemplate('cluster', $._config.dashboard_variables.cluster_query, '%s' % $._config.per_cluster_label, sort=sortAscending)
        .addMultiTemplate('namespace', $._config.dashboard_variables.namespace_query, '%s' % $._config.per_namespace_label, sort=sortAscending, includeAll=false),
    },

  jobMatcher()::
    '%s=~"$cluster", %s=~"%s(%s)"' % [$._config.per_cluster_label, $._config.per_job_label, $._config.job_prefix, $._config.rollout_operator_container_name],

  namespaceMatcher()::
    '%s=~"$cluster", %s=~"$namespace"' % [$._config.per_cluster_label, $._config.per_namespace_label],

  units(units):: {
    fieldConfig+: {
      defaults+: {
        unit: units,
      },
    },
  },

  min(value):: {
    fieldConfig+: {
      defaults+: {
        min: 0,
      },
    },
  },

  hideLegend:: {
    options+: {
      legend+: {
        showLegend: false,
      },
    },
  },

  showAllSeriesInTooltip:: {
    options+: {
      tooltip+: {
        mode: 'multi',
      },
    },
  },

  timeseriesPanel(title)::
    super.timeseriesPanel(title) + {
      fieldConfig+: {
        defaults+: {
          unit: 'short',
          min: 0,
        },
      },
      options+: {
        tooltip+: {
          mode: 'multi',
        },
      },
    },

  qpsPanel(selector, statusLabelName='status_code')::
    super.qpsPanel(selector, statusLabelName) +
    $.aliasColors({
      '1xx': $._colors.warning,
      '2xx': $._colors.success,
      '3xx': '#6ED0E0',
      '4xx': '#EF843C',
      '5xx': $._colors.failed,
      OK: $._colors.success,
      success: $._colors.success,
      'error': $._colors.failed,
      cancel: '#A9A9A9',
      Canceled: '#A9A9A9',
    }) + {
      fieldConfig+: {
        defaults+: { unit: 'reqps' },
      },
    },

  latencyPanel(metricName, selector, multiplier='1e3')::
    super.latencyPanel(metricName, selector, multiplier) + {
      fieldConfig+: {
        defaults+: { unit: 'ms' },
      },
    },

  resourceUtilizationAndLimitLegend(resourceName)::
    [resourceName, 'limit', 'request'],

  resourceUtilizationQuery(metric)::
    $._config.rollout_operator_resources_panel_queries['%s_usage' % metric] % {
      instanceLabel: $._config.per_instance_label,
      namespace: $.namespaceMatcher(),
      instanceName: $._config.rollout_operator_instance_matcher,
      containerName: $._config.rollout_operator_container_name,
    },

  resourceUtilizationAndLimitQueries(metric)::
    [
      $.resourceUtilizationQuery(metric),
      $._config.rollout_operator_resources_panel_queries['%s_limit' % metric] % {
        namespace: $.namespaceMatcher(),
        containerName: $._config.rollout_operator_container_name,
      },
      $._config.rollout_operator_resources_panel_queries['%s_request' % metric] % {
        namespace: $.namespaceMatcher(),
        containerName: $._config.rollout_operator_container_name,
      },
    ],

  containerCPUUsagePanel::
    $.timeseriesPanel('CPU') +
    $.queryPanel($.resourceUtilizationAndLimitQueries('cpu'), $.resourceUtilizationAndLimitLegend('{{%s}}' % $._config.per_instance_label)) +
    $.showAllTooltip +
    {
      fieldConfig+: {
        overrides+: [
          resourceRequestStyle,
          resourceLimitStyle,
        ],
        defaults+: {
          unit: 'short',
          custom+: {
            fillOpacity: 0,
          },
        },
      },
    },

  containerMemoryWorkingSetPanel::
    $.timeseriesPanel('Memory (workingset)') +
    $.queryPanel($.resourceUtilizationAndLimitQueries('memory_working'), $.resourceUtilizationAndLimitLegend('{{%s}}' % $._config.per_instance_label)) +
    $.showAllTooltip +
    {
      fieldConfig+: {
        overrides+: [
          resourceRequestStyle,
          resourceLimitStyle,
        ],
        defaults+: {
          unit: 'bytes',
          custom+: {
            fillOpacity: 0,
          },
        },
      },
    },

  // Shows all series' values in the tooltip and sorts them in descending order.
  showAllTooltip:: {
    options+: {
      tooltip+: {
        mode: 'multi',
        sort: 'desc',
      },
    },
  },

  panelDescription(title, description):: {
    description: |||
      ### %s
      %s
    ||| % [title, description],
  },

  // Panel query override functions
  overrideField(matcherId, options, overrideProperties):: {
    matcher: {
      id: matcherId,
      options: options,
    },
    properties: overrideProperties,
  },

  overrideFieldByName(fieldName, overrideProperties)::
    $.overrideField('byName', fieldName, overrideProperties),

  overrideProperty(id, value):: { id: id, value: value },

  aliasColors(colors):: {
    // aliasColors was the configuration in (deprecated) graph panel; we hide it from JSON model.
    aliasColors:: super.aliasColors,
    fieldConfig+: {
      overrides+: [
        $.overrideFieldByName(name, [
          $.overrideProperty('color', { mode: 'fixed', fixedColor: colors[name] }),
        ])
        for name in std.objectFields(colors)
      ],
    },
  },
}
