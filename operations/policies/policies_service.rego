package main

# Tests which relate to the Service manifest

has_port(obj, name, port, target_port, protocol) if {
    p := obj.spec.ports[_]
    p.name == name
    p.port == port
    p.targetPort == target_port
    p.protocol == protocol
}

# Assert that the Role has expected rules
deny contains msg if {
	obj := input[_].contents
	is_service(obj)
    not has_port(obj, "https", 443, 8443, "TCP")
    msg := sprintf("Service does not have expected port, %v", [display_name(obj)])
}