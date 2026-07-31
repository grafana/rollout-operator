local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    rollout_operator_webhooks_enabled: true,
    rollout_operator_replica_template_access_enabled: true,
    zpdb_custom_resource_definition_enabled: false,
    replica_template_custom_resource_definition_enabled: false,
  },
}
