package main

# Tests which relate to the webhook configurations

webhook_rule_present(obj, expected) if {
  rule := obj.webhooks[0].rules[_]
  rule.apiGroups == expected.apiGroups
  rule.apiVersions == expected.apiVersions
  rule.operations == expected.operations
  rule.resources == expected.resources
  rule.scope == "Namespaced"
}

# Assert that all WebhookConfigurations have the expected inject-rollout-operator-ca label
deny contains msg if {
    obj := input[_].contents
    obj.kind in { "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration" }
    object.get(obj.metadata.labels, "grafana.com/inject-rollout-operator-ca", "false") != "true"
    msg := sprintf("WebhookConfiguration should have label grafana.com/inject-rollout-operator-ca=true, %v", [display_name(obj)])
}

# Assert that all WebhookConfigurations clientConfig.service.namespace should match the grafana.com/namespace label
deny contains msg if {
    obj := input[_].contents
    obj.kind in { "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration" }
    obj.webhooks[0].clientConfig.service.namespace != object.get(obj.metadata.labels, "grafana.com/namespace", "")
    msg := sprintf("WebhookConfiguration clientConfig.service.name should match grafana.com/namespace label, %v. %v != %v", [display_name(obj), obj.webhooks[0].clientConfig.service.namespace, object.get(obj.metadata.labels, "grafana.com/namespace", "")])
}

# Assert that all WebhookConfigurations clientConfig.service.port matches the service port
deny contains msg if {
    obj := input[_].contents
    obj.kind in { "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration" }
    svc := input[_].contents
    is_service(svc)
    servicePort := svc.spec.ports[_]
    servicePort.name == "https"
    obj.webhooks[0].clientConfig.service.port != servicePort.port
    msg := sprintf("WebhookConfiguration has unexpected clientConfig.service.port, %v. Expected %v, got %v", [display_name(obj), servicePort.port, obj.webhooks[0].clientConfig.service.port])
}

# Assert that all WebhookConfigurations clientConfig.service.name matches the service name
deny contains msg if {
    obj := input[_].contents
     obj.kind in { "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration" }
    svc := input[_].contents
    is_service(svc)
    obj.webhooks[0].clientConfig.service.name != svc.metadata.name
    msg := sprintf("WebhookConfiguration client config service name does not match, %v. Got %v, expected %v", [display_name(obj), obj.webhooks[0].clientConfig.service.name, svc.metadata.name])
}

# Assert that all ValidatingWebhookConfiguration declare no side effects
deny contains msg if {
    obj := input[_].contents
    obj.kind == "ValidatingWebhookConfiguration"
    obj.webhooks[0].sideEffects != "None"
    msg := sprintf("ValidatingWebhookConfiguration has unexpected webhook sideEffect , %v. Expected None, got %v", [display_name(obj), obj.webhooks[0].sideEffects])
}

# Assert that all MutatingWebhookConfiguration declare no side effects on dry run
deny contains msg if {
    obj := input[_].contents
    obj.kind == "MutatingWebhookConfiguration"
    obj.webhooks[0].sideEffects != "NoneOnDryRun"
    msg := sprintf("MutatingWebhookConfiguration has unexpected webhook sideEffect , %v. Expected NoneOnDryRun, got %v", [display_name(obj), obj.webhooks[0].sideEffects])
}

# Assert that the prepare-downscale has expected path
deny contains msg if {
    path := "/admission/prepare-downscale"
	obj := input[_].contents
	is_prepare_downscale_webhook(obj)
	obj.webhooks[0].clientConfig.service.path != path
    msg := sprintf("MutatingWebhookConfiguration does not have expected path, %v. Expected %v, got %v", [display_name(obj), path, obj.webhooks[0].clientConfig.service.path])
}

# Assert that the prepare-downscale has expected rule
deny contains msg if {
    rule := { "apiGroups": ["apps"], "apiVersions": ["v1"], "operations": ["UPDATE"], "resources": ["statefulsets", "statefulsets/scale"] }
	obj := input[_].contents
	is_prepare_downscale_webhook(obj)
	not webhook_rule_present(obj, rule)
    msg := sprintf("MutatingWebhookConfiguration does not have expected rule, %v. Expected %v, got %v", [display_name(obj), rule, obj.webhooks[0].rules])
}

# Assert that the no-downscale has expected path
deny contains msg if {
    path := "/admission/no-downscale"
	obj := input[_].contents
	is_no_downscale_webhook(obj)
	obj.webhooks[0].clientConfig.service.path != path
    msg := sprintf("ValidatingWebhookConfiguration does not have expected path, %v. Expected %v, got %v", [display_name(obj), path, obj.webhooks[0].clientConfig.service.path])
}

# Assert that the no-downscale has expected rules
deny contains msg if {
    rule := { "apiGroups": ["apps"], "apiVersions": ["v1"], "operations": ["UPDATE"], "resources": ["statefulsets", "statefulsets/scale"] }
	obj := input[_].contents
	is_no_downscale_webhook(obj)
    not webhook_rule_present(obj, rule)
    msg := sprintf("ValidatingWebhookConfiguration does not have expected rule, %v. Expected %v, got %v", [display_name(obj), rule, obj.webhooks[0].rules])
}

# Assert that the pod-eviction has expected path
deny contains msg if {
    path := "/admission/pod-eviction"
	obj := input[_].contents
	is_pod_eviction_webhook(obj)
	obj.webhooks[0].clientConfig.service.path != path
    msg := sprintf("ValidatingWebhookConfiguration does not have expected path, %v. Expected %v, got %v", [display_name(obj), path, obj.webhooks[0].clientConfig.service.path])
}

# Assert that the pod-eviction has expected rules
deny contains msg if {
    rule := { "apiGroups": [""], "apiVersions": ["v1"], "operations": ["CREATE"], "resources": ["pods/eviction"] }
	obj := input[_].contents
	is_pod_eviction_webhook(obj)
    not webhook_rule_present(obj, rule)
    msg := sprintf("ValidatingWebhookConfiguration does not have expected rule, %v. Expected %v, got %v", [display_name(obj), rule, obj.webhooks[0].rules])
}


# Assert that the zpdb-validation has expected path
deny contains msg if {
    path := "/admission/zpdb-validation"
	obj := input[_].contents
	is_zpdb_validation_webhook(obj)
	obj.webhooks[0].clientConfig.service.path != path
    msg := sprintf("ValidatingWebhookConfiguration does not have expected path, %v. Expected %v, got %v", [display_name(obj), path, obj.webhooks[0].clientConfig.service.path])
}

# Assert that the zpdb-validation has expected rules
deny contains msg if {
    rule := { "apiGroups": ["rollout-operator.grafana.com"], "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"], "resources": ["zoneawarepoddisruptionbudgets"] }
	obj := input[_].contents
	is_zpdb_validation_webhook(obj)
    not webhook_rule_present(obj, rule)
    msg := sprintf("ValidatingWebhookConfiguration does not have expected rule, %v. Expected %v, got %v", [display_name(obj), rule, obj.webhooks[0].rules])
}