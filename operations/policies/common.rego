package main

object_display_name[i] := display_name if {
	contents := input[i].contents
	contents != null
	kind := object.get(contents, "kind", "<unknown>")
	name := object.get(object.get(contents, "metadata", {}), "name", "<unknown>")
	display_name := sprintf("%v/%v", [kind, name])
}

has_key(x, k) if {
	_ = x[k]
}

# Assert that a role rule can be found
rule_present(obj, expected) if {
  some r
  rule := obj.rules[r]
  rule.apiGroups == expected.apiGroups
  rule.resources == expected.resources
  rule.verbs == expected.verbs
  # resourceNames is not on most rules
  object.get(rule, "resourceNames", "") == object.get(expected, "resourceNames", "")
}

# True if there are webhooks in the configuration
is_webhooks_deployment if {
    some i
    obj := input[i].contents
    obj.kind == "ValidatingWebhookConfiguration"
}

has_service_account if {
    some i
    obj := input[i].contents
    obj.kind == "ServiceAccount"
    obj.metadata.name = "rollout-operator"
}

has_service if {
    some i
    obj := input[i].contents
    obj.kind == "Service"
    obj.metadata.name = "rollout-operator"
}

has_role if {
    some i
    obj := input[i].contents
    obj.kind == "Role"
    obj.metadata.name == "rollout-operator-role"
}

has_role_binding if {
    some i
    obj := input[i].contents
    obj.kind == "RoleBinding"
    obj.metadata.name == "rollout-operator-rolebinding"
}

has_deployment if {
    some i
    obj := input[i].contents
    obj.kind == "Deployment"
    obj.metadata.name == "rollout-operator"
}

has_zpdb_crd if {
    some i
    obj := input[i].contents
    obj.kind == "CustomResourceDefinition"
    obj.metadata.name == "zoneawarepoddisruptionbudgets.rollout-operator.grafana.com"
}

has_cluster_role if {
    some i
    obj := input[i].contents
    obj.kind == "ClusterRole"
    obj.metadata.name == "rollout-operator-default-webhook-cert-update-role"
}

has_cluster_role_binding if {
    some i
    obj := input[i].contents
    obj.kind == "ClusterRoleBinding"
    obj.metadata.name == "rollout-operator-default-webhook-cert-secret-rolebinding"
}

has_webhooks_cert_role if {
    some i
    obj := input[i].contents
    obj.kind == "Role"
    obj.metadata.name == "rollout-operator-webhook-cert-secret-role"
}

has_webhooks_cert_role_binding if {
    some i
    obj := input[i].contents
    obj.kind == "RoleBinding"
    obj.metadata.name == "rollout-operator-webhook-cert-secret-rolebinding"
}

has_prepare_downscale_webhook if {
    some i
    obj := input[i].contents
    obj.kind == "MutatingWebhookConfiguration"
    obj.metadata.name == "prepare-downscale-default"
}

has_no_downscale_webhook if {
    some i
    obj := input[i].contents
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "no-downscale-default"
}

has_pod_eviction_webhook if {
    some i
    obj := input[i].contents
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "pod-eviction-default"
}

has_zpdb_validation_webhook if {
    some i
    obj := input[i].contents
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "zpdb-validation-default"
}



