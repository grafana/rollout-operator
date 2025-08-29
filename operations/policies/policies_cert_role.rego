package main

# Tests which relate to the Role and RoleBinding used for secrets/certificate access

expected_cert_role_rules = [
  {
    "apiGroups": [""],
    "resources": ["secrets"],
    "verbs": ["create"],
  },
  {
     "apiGroups": [""],
     "resourceNames": ["rollout-operator-self-signed-certificate"],
     "resources": ["secrets"],
     "verbs": ["update", "get"],
  },
]

is_rollout_operator_cert_role(obj) if {
    obj.kind == "Role"
    obj.metadata.name == "rollout-operator-webhook-cert-secret-role"
}

is_rollout_operator_cert_role_binding(obj) if {
    obj.kind == "RoleBinding"
    obj.metadata.name == "rollout-operator-webhook-cert-secret-rolebinding"
}

validate_cert_role_binding(obj) if {
    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "Role"
    obj.roleRef.name == "rollout-operator-webhook-cert-secret-role"

    obj.subjects[0].kind == "ServiceAccount"
    obj.subjects[0].name == "rollout-operator"
    obj.subjects[0].namespace == "default"
}

# Assert that the Role has expected rules
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_cert_role(obj)

    some j
    expected := expected_cert_role_rules[j]
    not rule_present(obj, expected)
    msg := sprintf("Role does not have expected rule, %v. expected=%v", [object_display_name[i], expected])
}

# Assert that the RoleBinding has expected attributes
deny contains msg if {
	obj := input[i].contents
	is_rollout_operator_cert_role_binding(obj)
    not validate_cert_role_binding(obj)
    msg := sprintf("RoleBinding does not have expected attributes, %v", [object_display_name[i]])
}