local mimir = import 'rollout-operator/rollout-operator.libsonnet';

mimir {
  _config+:: {
    namespace: 'default',
    // test that enabling the zpdb will implicitly enables the rollout-operator and the webhooks
    enable_zpdb: true,
  },
}
