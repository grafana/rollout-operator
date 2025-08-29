package main

# Tests which relate to the expected custom resource definition manifests

is_zpdb_crd(obj) if {
    obj.kind == "CustomResourceDefinition"
    obj.metadata.name == "zoneawarepoddisruptionbudgets.rollout-operator.grafana.com"
}

# Assert that the zpbd custom resource definition has key attributes
validate_zpdb_crd(obj) if {
    obj.spec.group == "rollout-operator.grafana.com"
    obj.spec.names.kind == "ZoneAwarePodDisruptionBudget"
    obj.spec.names.plural == "zoneawarepoddisruptionbudgets"
    obj.spec.names.singular == "zoneawarepoddisruptionbudget"
    obj.spec.names.shortNames[0] == "zpdb"
    obj.spec.scope == "Namespaced"
    obj.spec.versions[0].name == "v1"
    object.get(obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties, "maxUnavailable", null) != null
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.maxUnavailable.minimum == 0
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.maxUnavailable.type == "integer"
    object.get(obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties, "maxUnavailablePercentage", null) != null
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.maxUnavailablePercentage.minimum == 0
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.maxUnavailablePercentage.maximum == 100
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.maxUnavailablePercentage.type == "integer"
    object.get(obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties, "podNamePartitionRegex", null) != null
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.podNamePartitionRegex.type == "string"
    object.get(obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties, "podNameRegexGroup", null) != null
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.podNameRegexGroup.minimum == 1
    obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.podNameRegexGroup.type == "integer"
    object.get(obj.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties, "selector", null) != null
}

# Assert that the custom resource definition has expected attributes
deny contains msg if {
	obj := input[i].contents
	is_zpdb_crd(obj)
    not validate_zpdb_crd(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget CRD does not have expected attributes, %v, %s", [object_display_name[i], obj])
}