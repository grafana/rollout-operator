package main

# Tests which relate to the expected Role and RoleBinding manifests

expected_rules = [
  {
    "apiGroups": [""],
    "resources": ["pods"],
    "verbs": ["list", "get", "watch", "delete"],
  },
  {
    "apiGroups": ["apps"],
    "resources": ["statefulsets"],
    "verbs": ["list", "get", "watch", "patch"],
  },
  {
    "apiGroups": ["apps"],
    "resources": ["statefulsets/status"],
    "verbs": ["update"],
  },
  {
    "apiGroups": [""],
    "resources": ["configmaps"],
    "verbs": ["get", "update", "create"],
  },
]

extra_zpdb_rule = {
    "apiGroups": ["rollout-operator.grafana.com"],
    "resources": ["zoneawarepoddisruptionbudgets"],
    "verbs": ["get", "list", "watch"],
}

extra_replica_template_rule = {
    "apiGroups": ["rollout-operator.grafana.com"],
    "resources": ["replicatemplates/scale", "replicatemplates/status"],
    "verbs": ["get", "patch"],
}

role_binding_validation(obj) if {
    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "Role"
    obj.subjects[0].kind == "ServiceAccount"
}

# Assert that the Role has expected rules
deny contains msg if {
	obj := input[_].contents
	is_role(obj)
    expected := expected_rules[_]
    not rule_present(obj, expected)
    msg := sprintf("Role does not have expected rule, %v. Got %v, expected=%v", [display_name(obj), obj.rules, expected])
}

# Assert that the Role has an extra rule when webhooks are used
deny contains msg if {
	obj := input[_].contents
	has_webhooks_deployment
	is_role(obj)
    not rule_present(obj, extra_zpdb_rule)
    msg := sprintf("Role does not have expected zpdb rule, %v. Got %v, expected=%v", [display_name(obj), obj.rules, extra_zpdb_rule])
}

# Assert that the Role has an extra rule when replica templates are used
deny contains msg if {
	obj := input[_].contents
	has_replica_template_deployment
	is_role(obj)
    not rule_present(obj, extra_replica_template_rule)
    msg := sprintf("Role does not have expected replica template rule, %v. Got %v, expected=%v", [display_name(obj), obj.rules, extra_replica_template_rule])
}

# Assert on RoleBinding has expected attributes
deny contains msg if {
	obj := input[_].contents
	is_role_binding(obj)
	not role_binding_validation(obj)
	msg := sprintf("RoleBinding does not have expected attributes, %v %v", [display_name(obj), obj])
}

# Assert that the RoleBinding subject ServiceAccount name matches
deny contains msg if {
    role_binding := input[_].contents
    is_role_binding(role_binding)
    account := input[_].contents
    is_service_account(account)
    account.metadata.name != role_binding.subjects[0].name
    msg := sprintf("RoleBinding subject ServiceName does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.subjects[0].name, account.metadata.name])
}

# Assert that the RoleBinding roleRef matches
deny contains msg if {
    role_binding := input[_].contents
    is_role_binding(role_binding)
    role := input[_].contents
    is_role(role)
    role.metadata.name != role_binding.roleRef.name
    msg := sprintf("RoleBinding roleRef name does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.roleRef.name, role.metadata.name])
}