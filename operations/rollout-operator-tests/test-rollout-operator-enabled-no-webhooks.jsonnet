local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    rollout_operator_replica_template_access_enabled: false,
    rollout_operator_webhooks_enabled: false,
  },
}
