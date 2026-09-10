local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
  },
}
