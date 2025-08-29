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

is_rollout_operator_role(obj) if {
    obj.kind == "Role"
    obj.metadata.name == "rollout-operator-role"
}

is_rollout_operator_role_found if {
  some i
  obj := input[i].contents
  is_rollout_operator_role(obj)
}

is_rollout_operator_role_binding(obj) if {
    obj.kind == "RoleBinding"
    obj.metadata.name == "rollout-operator-rolebinding"
}

is_rollout_operator_role_binding_found if {
  some i
  obj := input[i].contents
  is_rollout_operator_role_binding(obj)
}

role_binding_validation(obj) if {
    obj.metadata.name == "rollout-operator-rolebinding"

    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "Role"
    obj.roleRef.name == "rollout-operator-role"

    obj.subjects[0].kind == "ServiceAccount"
    obj.subjects[0].name == "rollout-operator"
    obj.subjects[0].namespace == "default"
}


# Assert that the Role has expected rules
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_role(obj)

    some j
    expected := expected_rules[j]
    not rule_present(obj, expected)
    msg := sprintf("Role does not have expected rule, %v. expected=%v", [object_display_name[i], expected])
}

# Assert that the Role has the extra rule when zpdb is used
deny contains msg if {
	obj := input[i].contents
	is_webhooks_deployment
	is_rollout_operator_role(obj)
    not rule_present(obj, extra_zpdb_rule)
    msg := sprintf("Role does not have expected rule, %v. expected=%v", [object_display_name[i], extra_zpdb_rule])
}

# TODO check for extra replicate template rule if there are any of it's configs found

# Assert on RoleBinding has expected attributes
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_role_binding(obj)
	not role_binding_validation(obj)
	msg := sprintf("RoleBinding does not have expected attributes, %v %v", [object_display_name[i], obj])
}
