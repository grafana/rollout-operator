local rollout_operator = import 'rollout-operator/rollout-operator.libsonnet';

local manifests = rollout_operator {
  _config+:: {
    namespace: 'default',
    rollout_operator_leader_election_enabled: true,
  },
};

assert manifests.rollout_operator_args['leader-election.enabled'] == true;
assert manifests.rollout_operator_container.env == [{ name: 'POD_NAME', valueFrom: { fieldRef: { fieldPath: 'metadata.name' } } }];
manifests
