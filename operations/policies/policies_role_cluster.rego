package main

# Tests which relate to the ClusterRole and ClusterRoleBinding attributes

expected_cluster_role_rules = [
  {
    "apiGroups": ["admissionregistration.k8s.io"],
    "resources": ["validatingwebhookconfigurations", "mutatingwebhookconfigurations"],
    "verbs": ["list", "patch", "watch"],
  },
]

validate_cluster_role_binding(obj) if {
    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "ClusterRole"
    obj.subjects[_].kind == "ServiceAccount"
}

# Assert that the ClusterRole has expected rules
deny contains msg if {
	obj := input[_].contents
	is_cluster_role(obj)
    expected := expected_cluster_role_rules[_]
    not rule_present(obj, expected)
    msg := sprintf("ClusterRole does not have expected rule, %v. Got %v, expected=%v", [display_name(obj), obj.rules, expected])
}

# Assert that the ClusterRoleBinding has expected attributes
deny contains msg if {
	obj := input[_].contents
	is_cluster_role_binding(obj)
    not validate_cluster_role_binding(obj)
    msg := sprintf("ClusterRoleBinding does not have expected attributes, %v", [display_name(obj)])
}

# Assert that the ClusterRoleBinding subject ServiceAccount matches
deny contains msg if {
    role_binding := input[_].contents
    is_cluster_role_binding(role_binding)
    account := input[_].contents
    is_service_account(account)
    account.metadata.name != role_binding.subjects[0].name
    msg := sprintf("ClusterRoleBinding subject ServiceAccount name does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.subjects[0].name, account.metadata.name])
}

# Assert that the ClusterRoleBinding roleRef Role matches
deny contains msg if {
    role_binding := input[_].contents
    is_cluster_role_binding(role_binding)
    role := input[_].contents
    is_cluster_role(role)
    role.metadata.name != role_binding.roleRef.name
    msg := sprintf("ClusterRoleBinding roleRef name does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.roleRef.name, role.metadata.name])
}