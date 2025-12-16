{
  prometheusAlerts+::
    { _config:: $._config + $._group_config } +
    (import 'alerts/alerts.libsonnet'),
}
