local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    zpdb_custom_resource_definition_enabled: true,
  },
  ingester_rollout_pdb: $.newZPDB('mock-rollout', 'mock', 2),
}
