package main

display_name(obj) := display_name if {
    obj != null
	kind := object.get(obj, "kind", "<unknown>")
	name := object.get(object.get(obj, "metadata", {}), "name", "<unknown>")
	display_name := sprintf("%v/%v", [kind, name])
}

has_key(x, k) if {
	_ = x[k]
}

role_api_group_exists(role, api_group) if {
    role.rules[_].apiGroups[_] == api_group
}

# Assert that a role rule can be found
rule_present(obj, expected) if {
  rule := obj.rules[_]
  rule.apiGroups == expected.apiGroups
  rule.resources == expected.resources
  rule.verbs == expected.verbs
  # resourceNames is not on most rules
  object.get(rule, "resourceNames", "") == object.get(expected, "resourceNames", "")
}

is_service_account(obj) if {
    obj.kind == "ServiceAccount"
}

is_service(obj) if {
    obj.kind == "Service"
}

# True if there are webhooks in the configuration
has_webhooks_deployment if {
    input[_].contents.kind == "ValidatingWebhookConfiguration"
}

has_replica_template_deployment if {
    is_replica_template_deployment(input[_].contents)
}

is_replica_template_deployment(obj) if {
    obj.kind == "ReplicaTemplate"
}

has_zpdb_deployment if {
    is_zpdb_deployment(input[_].contents)
}

is_zpdb_deployment(obj) if {
    obj.kind == "ZoneAwarePodDisruptionBudget"
}

has_service_account if {
    input[_].contents.kind == "ServiceAccount"
}

has_service if {
    input[_].contents.kind == "Service"
}

has_deployment if {
    is_deployment(input[_].contents)
}

is_deployment(obj) if {
   obj.kind == "Deployment"
}

has_zpdb_crd if {
    is_zpdb_crd(input[_].contents)
}

is_zpdb_crd(obj) if {
    obj.kind == "CustomResourceDefinition"
    startswith(obj.metadata.name, "zoneawarepoddisruptionbudgets")
}

is_replica_template_crd(obj) if {
    obj.kind == "CustomResourceDefinition"
    startswith(obj.metadata.name, "replicatemplates")
}

has_role if {
    is_role(input[_].contents)
}

is_role(obj) if {
    obj.kind == "Role"
    obj.metadata.name == "rollout-operator-role"
}

has_role_binding if {
    is_role_binding(input[_].contents)
}

is_role_binding(obj) if {
    obj.kind == "RoleBinding"
    obj.metadata.name == "rollout-operator-rolebinding"
}

has_cluster_role if {
    is_cluster_role(input[_].contents)
}

is_cluster_role(obj) if {
   obj.kind == "ClusterRole"
}

has_cluster_role_binding if {
   is_cluster_role_binding(input[_].contents)
}

is_cluster_role_binding(obj) if {
    obj.kind == "ClusterRoleBinding"
}

has_webhooks_cert_role if {
    is_webhooks_cert_role(input[_].contents)
}

is_webhooks_cert_role(obj) if {
    obj.kind == "Role"
    endswith(obj.metadata.name, "-webhook-cert-secret-role")
}

has_webhooks_cert_role_binding if {
    is_webhooks_cert_role_binding(input[_].contents)
}

is_webhooks_cert_role_binding(obj) if {
    obj.kind == "RoleBinding"
    endswith(obj.metadata.name, "-webhook-cert-secret-rolebinding")
}

has_prepare_downscale_webhook if {
    is_prepare_downscale_webhook(input[_].contents)
}

is_prepare_downscale_webhook(obj) if {
    obj.kind == "MutatingWebhookConfiguration"
    startswith(obj.metadata.name, "prepare-downscale-")
}

has_no_downscale_webhook if {
    is_no_downscale_webhook(input[_].contents)
}

is_no_downscale_webhook(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    startswith(obj.metadata.name, "no-downscale-")
}

has_pod_eviction_webhook if {
    is_pod_eviction_webhook(input[_].contents)
}

is_pod_eviction_webhook(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    startswith(obj.metadata.name, "pod-eviction-")
}

has_zpdb_validation_webhook if {
    is_zpdb_validation_webhook(input[_].contents)
}

is_zpdb_validation_webhook(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    startswith(obj.metadata.name, "zpdb-validation-")
}