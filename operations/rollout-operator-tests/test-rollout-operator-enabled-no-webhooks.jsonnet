local mimir = import 'rollout-operator/rollout-operator.libsonnet';

mimir {
  _config+:: {
    namespace: 'default',
    rollout_operator_enabled: true,
  },
}
