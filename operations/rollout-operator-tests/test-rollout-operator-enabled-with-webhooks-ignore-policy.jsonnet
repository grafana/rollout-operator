local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

rollout_operator {
  _config+:: {
    namespace: 'default',
    ignore_rollout_operator_no_downscale_webhook_failures: true,
    ignore_rollout_operator_prepare_downscale_webhook_failures: true,
    ignore_rollout_operator_zpdb_eviction_webhook_failures: true,
    ignore_rollout_operator_zpdb_validation_webhook_failures: true,
  },
}
