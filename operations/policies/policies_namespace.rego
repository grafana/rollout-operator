package main

import future.keywords
import future.keywords.every

# Tests which relate to the namespace being set in the expected places

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
	obj := input[_].contents
	msg := sprintf("Resource doesn't have a namespace %v", [display_name(obj)])

	should_be_namespaced(obj)
	not metadata_has_namespace(obj.metadata)
}

deny contains msg if {
	obj := input[_].contents
	msg := sprintf("Resource has a namespace, but shouldn't %v", [display_name(obj)])

	not should_be_namespaced(obj)
	metadata_has_namespace(obj.metadata)
}