local mimir = import 'rollout-operator/rollout-operator.libsonnet';

mimir {
  _config+:: {
    namespace: 'default',
    // test that enabling the webhooks will implicitly enable the rollout-operator
    enable_rollout_operator_webhook: true,
  },
}
