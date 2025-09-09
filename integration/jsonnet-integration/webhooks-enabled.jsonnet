local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    zpdb_custom_resource_definition_enabled: false,
    rollout_operator_webhooks_enabled: true,
  },
}
