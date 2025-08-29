package main

# Tests which relate to the Service manifest

is_service(obj) if {
    obj.kind == "Service"
    obj.metadata.name == "rollout-operator"
}

has_port(obj, name, port, target_port, protocol) if {
    some i
    obj.spec.ports[i].name == name
    obj.spec.ports[i].port == port
    obj.spec.ports[i].targetPort == target_port
    obj.spec.ports[i].protocol == protocol
}

# Assert that the Role has expected rules
deny contains msg if {
	obj := input[i].contents
	is_service(obj)
    not has_port(obj, "https", 443, 8443, "TCP")
    msg := sprintf("Service does not have expected port, %v", [object_display_name[i]])
}