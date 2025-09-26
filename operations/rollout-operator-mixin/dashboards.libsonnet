{
  grafanaDashboards+:
    (import 'dashboards/rollout-operator.libsonnet') + {
      _config:: (import '../config.libsonnet')._config,
    },
}
