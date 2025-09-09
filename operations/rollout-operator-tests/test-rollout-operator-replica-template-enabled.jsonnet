local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    rollout_operator_replica_template_access_enabled: true,
    replica_template_custom_resource_definition_enabled: true,
    zpdb_custom_resource_definition_enabled: false,
  },
  ingester_replica_template: $.replicaTemplate('ingester-zone-a', 0, 'name=ingester-zone-a'),
}
