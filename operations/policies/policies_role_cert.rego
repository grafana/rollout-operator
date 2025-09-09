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

validate_cert_role_binding(obj) if {
    obj.roleRef.apiGroup == "rbac.authorization.k8s.io"
    obj.roleRef.kind == "Role"
    obj.subjects[0].kind == "ServiceAccount"
}

# Assert that the Role has expected rules
deny contains msg if {
	obj := input[_].contents
	is_webhooks_cert_role(obj)
    expected := expected_cert_role_rules[_]
    not rule_present(obj, expected)
    msg := sprintf("Role does not have expected rule, %v. Got %v, expected=%v", [display_name(obj), obj.rules, expected])
}

# Assert that the RoleBinding has expected attributes
deny contains msg if {
	obj := input[_].contents
	is_webhooks_cert_role_binding(obj)
    not validate_cert_role_binding(obj)
    msg := sprintf("RoleBinding does not have expected attributes, %v", [display_name(obj)])
}

# Assert that the RoleBinding subject ServiceAccount matches
deny contains msg if {
    role_binding := input[_].contents
    is_webhooks_cert_role_binding(role_binding)
    account := input[_].contents
    is_service_account(account)
    account.metadata.name != role_binding.subjects[0].name
    msg := sprintf("RoleBinding subject ServiceName does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.subjects[0].name, account.metadata.name])
}

# Assert that the RoleBinding roleRef Role matches
deny contains msg if {
    role_binding := input[_].contents
    is_webhooks_cert_role_binding(role_binding)
    role := input[_].contents
    is_webhooks_cert_role(role)
    role.metadata.name != role_binding.roleRef.name
    msg := sprintf("RoleBinding roleRef name does not match, %v. Got %v, expected %v", [display_name(role_binding), role_binding.roleRef.name, role.metadata.name])
}