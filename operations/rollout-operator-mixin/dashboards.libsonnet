{
  grafanaDashboards+:
    (import 'dashboards/rollout-operator.libsonnet')($._config),
}
