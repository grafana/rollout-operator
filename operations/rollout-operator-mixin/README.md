# Rollout-operator mixin

The mixin provides Prometheus alerts and Grafana dashboards for rollout-operator.

## Observer alerts

Both observer alerts are enabled by default in the mixin. For deployments that intentionally do not use one or both CRDs, disable the corresponding alert:

```jsonnet
_config+:: {
    rollout_operator_zpdb_config_observer_alert_enabled: false,
    rollout_operator_replica_template_observer_alert_enabled: false,
}
```

These alerts require the readiness metrics to be scraped. They do not detect missing metrics or an unavailable operator.

See the [alert runbooks](../../docs/runbooks.md#rollout-operatorzpdbconfigobservernotready) for investigation steps.
