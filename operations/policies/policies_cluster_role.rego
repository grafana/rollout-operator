package main

# Tests which relate to the ClusterRole and ClusterRoleBinding attributes

expected_cluster_role_rules = [
  {
    "apiGroups": ["admissionregistration.k8s.io"],
    "resources": ["validatingwebhookconfigurations", "mutatingwebhookconfigurations"],
    "verbs": ["list", "patch", "watch"],
  },
]

is_rollout_operator_cluster_role(obj) if {
    obj.kind == "ClusterRole"
    obj.metadata.name == "rollout-operator-default-webhook-cert-update-role"
}

is_rollout_operator_cluster_role_binding(obj) if {
    obj.kind == "ClusterRoleBinding"
    obj.metadata.name == "rollout-operator-default-webhook-cert-secret-rolebinding"
}

validate_cluster_role_binding(obj) if {
    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "ClusterRole"
    obj.roleRef.name == "rollout-operator-default-webhook-cert-update-role"

    obj.subjects[0].kind == "ServiceAccount"
    obj.subjects[0].name == "rollout-operator"
    obj.subjects[0].namespace == "default"
}

# Assert that the ClusterRole has expected rules
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_cluster_role(obj)

    some j
    expected := expected_cluster_role_rules[j]
    not rule_present(obj, expected)
    msg := sprintf("ClusterRole does not have expected rule, %v. expected=%v", [object_display_name[i], expected])
}

# Assert that the ClusterRoleBinding has expected rules
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_cluster_role_binding(obj)
    not validate_cluster_role_binding(obj)
    msg := sprintf("ClusterRoleBinding does not have expected attributes, %v", [object_display_name[i]])
}
