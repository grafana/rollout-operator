# Rollout-operator mixin

The mixin provides Prometheus alerts and Grafana dashboards for rollout-operator.

## Observer alerts

The ZPDB observer alert is enabled by default. Disable it where the ZPDB CRD is intentionally unused. The ReplicaTemplate observer alert is disabled by default; enable it where ReplicaTemplates are used:

```jsonnet
_config+:: {
    rollout_operator_zpdb_config_observer_alert_enabled: false,
    rollout_operator_replica_template_observer_alert_enabled: true,
}
```

These alerts require the readiness metrics to be scraped. They do not detect missing metrics or an unavailable operator.

See the [alert runbooks](../../docs/runbooks.md#rollout-operatorzpdbconfigobservernotready) for investigation steps.
