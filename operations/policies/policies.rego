package main

import future.keywords
import future.keywords.every


should_be_namespaced(contents) if {
	# if we don't know the kind, then it should have a namespace
	not has_key(contents, "kind")
}

should_be_namespaced(contents) if {
	not contents.kind in ["ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition", "MutatingWebhookConfiguration", "Namespace", "PodSecurityPolicy", "ValidatingWebhookConfiguration"]
}

metadata_has_namespace(metadata) if {
	has_key(metadata, "namespace")
	regex.match(".+", metadata.namespace)
}

deny contains msg if {
	obj := input[i].contents
	msg := sprintf("Resource doesn't have a namespace %v", [object_display_name[i]])

	should_be_namespaced(obj)
	not metadata_has_namespace(obj.metadata)
}

deny contains msg if {
	obj := input[i].contents
	msg := sprintf("Resource has a namespace, but shouldn't %v", [object_display_name[i]])

	not should_be_namespaced(obj)
	metadata_has_namespace(obj.metadata)
}

deny contains msg if {
	obj := input[i].contents
	msg = sprintf("Resource has empty nodeSelector, but shouldn't %s", [object_display_name[i]])

	obj.kind in ["Deployment"]
	nodeSelector := obj.spec.template.spec.nodeSelector

	keys := object.keys(nodeSelector)
	count(keys) == 0
}

deny contains msg if {
	obj := input[i].contents
	msg := sprintf("ServiceAccount does not have expected name %v expected=%v got %v", [object_display_name[i], "rollout-operator", obj.metadata.name])

	obj.kind == "ServiceAccount"
	obj.metadata.name != "rollout-operator"
}