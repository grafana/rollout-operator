local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    rollout_operator_replica_template_access_enabled: true,
  },
  ingester_replica_template: $.replicaTemplate('ingester-zone-a', 5, 'name=ingester-zone-a'),
}
