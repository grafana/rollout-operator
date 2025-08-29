local mimir = import 'rollout-operator/rollout-operator.libsonnet';

mimir {
  _config+:: {
    namespace: 'default',
    // test that enabling the replica templates will implicitly enables the rollout-operator and the webhooks
    rollout_operator_replica_template_access_enabled: true,
    enable_zpdb: true,
  },
}
