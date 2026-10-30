# Rollout-operator mixin

The mixin provides Prometheus alerts and Grafana dashboards for rollout-operator.

## Observer alerts

Both observer alerts are disabled by default. Enable each alert where its CRD is used:

```jsonnet
_config+:: {
    rollout_operator_zpdb_config_observer_alert_enabled: true,
    rollout_operator_replica_template_observer_alert_enabled: true,
}
```

These alerts require the readiness metrics to be scraped. They do not detect missing metrics or an unavailable operator.

See the [alert runbooks](../../docs/runbooks.md#rollout-operatorzpdbconfigobservernotready) for investigation steps.
