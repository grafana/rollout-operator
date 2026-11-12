local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

local manifests = rollout_operator {
  _config+:: {
    namespace: 'default',
  },
};

assert !std.objectHas(manifests.rollout_operator_args, 'leader-election.enabled');
assert !std.objectHas(manifests.rollout_operator_container, 'env');
manifests
