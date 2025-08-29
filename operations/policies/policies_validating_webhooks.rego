package main

# Tests which relate to the Service manifest

is_prepare_downscale(obj) if {
    obj.kind == "MutatingWebhookConfiguration"
    obj.metadata.name == "prepare-downscale-default"
}

is_no_downscale(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "no-downscale-default"
}

is_pod_eviction(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "pod-eviction-default"
}

is_zpdb_validation(obj) if {
    obj.kind == "ValidatingWebhookConfiguration"
    obj.metadata.name == "zpdb-validation-default"
}

webhook_rule_present(obj, expected) if {
  some r
  rule := obj.webhooks[0].rules[r]
  rule.apiGroups == expected.apiGroups
  rule.apiVersions == expected.apiVersions
  rule.operations == expected.operations
  rule.resources == expected.resources
  rule.scope == "Namespaced"
}


validate_webhook(obj, name, path, side_effect, match_policy, timeout_secs, rule) if {
    object.get(obj.metadata.labels, "grafana.com/inject-rollout-operator-ca", "false") == "true"
    object.get(obj.metadata.labels, "grafana.com/namespace", "") == "default"
    obj.webhooks[0].name == name
    obj.webhooks[0].clientConfig.service.name == "rollout-operator"
    obj.webhooks[0].clientConfig.service.namespace == "default"
    obj.webhooks[0].clientConfig.service.path == path
    obj.webhooks[0].clientConfig.service.port == 443
    obj.webhooks[0].failurePolicy in {"Fail","Ignore"}
    object.get(obj.webhooks[0], "matchPolicy", "") == match_policy
    obj.webhooks[0].sideEffects == side_effect
    object.get(obj.webhooks[0], "timeoutSeconds", 0) == timeout_secs
    # object.get(obj.webhooks[0].namespaceSelector.matchLabels, "kubernetes.io/metadata.name", "")  == "default"
    # webhook_rule_present(obj, rule)
}

# Assert that the prepare-downscale MutatingWebhookConfiguration has expected attributes
deny contains msg if {
    rule := { "apiGroups": ["apps"], "apiVersions": ["v1"], "operations": ["UPDATE"], "resources": ["statefulsets", "statefulsets/scale"] }
	obj := input[i].contents
	is_prepare_downscale(obj)
    not validate_webhook(obj, "prepare-downscale-default.grafana.com", "/admission/prepare-downscale", "NoneOnDryRun", "Equivalent", 10, rule)
    msg := sprintf("prepare-downscale MutatingWebhookConfiguration does not have expected attributes, %v %v", [object_display_name[i], obj])
}

# Assert that the no-downscale ValidatingWebhookConfiguration has expected attributes
deny contains msg if {
    rule := { "apiGroups": ["apps"], "apiVersions": ["v1"], "operations": ["UPDATE"], "resources": ["statefulsets", "statefulsets/scale"] }
	obj := input[i].contents
	is_no_downscale(obj)
    not validate_webhook(obj, "no-downscale-default.grafana.com", "/admission/no-downscale", "None", "Equivalent", 10, rule)
    msg := sprintf("no-downscale ValidatingWebhookConfiguration does not have expected attributes, %v %v", [object_display_name[i], obj])
}

# Assert that the pod-eviction ValidatingWebhookConfiguration has expected attributes
deny contains msg if {
    rule := { "apiGroups": [""], "apiVersions": ["v1"], "operations": ["CREATE"], "resources": ["pods/eviction"] }
	obj := input[i].contents
	is_pod_eviction(obj)
    not validate_webhook(obj, "pod-eviction-default.grafana.com", "/admission/pod-eviction", "None", "", 0, rule)
    msg := sprintf("pod-eviction ValidatingWebhookConfiguration does not have expected attributes, %v %v", [object_display_name[i], obj])
}

# Assert that the zpdb-validation ValidatingWebhookConfiguration has expected attributes
deny contains msg if {
    rule := { "apiGroups": ["rollout-operator.grafana.com"], "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"], "resources": ["zoneawarepoddisruptionbudgets"] }
	obj := input[i].contents
	is_zpdb_validation(obj)
    not validate_webhook(obj, "zpdb-validation-default.grafana.com", "/admission/zpdb-validation", "None", "", 0, rule)
    msg := sprintf("zpdb-validation ValidatingWebhookConfiguration does not have expected attributes, %v %v", [object_display_name[i], obj])
}