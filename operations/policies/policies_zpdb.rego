package main

# Tests which relate to the expected zpdb configurations

is_max_available_zpdb(obj) if {
    is_zpdb_deployment(obj)
    object.get(obj.spec, "maxUnavailable", -1) >= 0
}

is_max_available_percentage_zpdb(obj) if {
    is_zpdb_deployment(obj)
    object.get(obj.spec, "maxUnavailablePercentage", -1) >= 0
}

is_regex_zpdb(obj) if {
    is_zpdb_deployment(obj)
    object.get(obj.spec, "podNamePartitionRegex", "") != ""
}

validate_zpdb_regex(obj) if {
    object.get(obj.spec, "podNamePartitionRegex", "") != ""
    object.get(obj.spec, "podNameRegexGroup", -1) >= 1
}

has_selector(obj) if {
    object.get(obj.spec.selector.matchLabels, "rollout-group", "") != null
}

deny contains msg if {
	obj := input[_].contents
	is_regex_zpdb(obj)
	not validate_zpdb_regex(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget does not have expected regex attributes, %v", [display_name(obj)])
}

# Assert that the custom resource definition has expected attributes
deny contains msg if {
	obj := input[_].contents
	is_zpdb_deployment(obj)
	not has_selector(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget does not have a selector set, %v", [display_name(obj)])
}

# Assert that only one maxUnavailable is set
deny contains msg if {
	obj := input[_].contents
	is_max_available_zpdb(obj)
	is_max_available_percentage_zpdb(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget should not have maxUnavailable and maxUnavailablePercentage set, %v", [display_name(obj)])
}

# Assert that only one maxUnavailable is set
deny contains msg if {
	obj := input[_].contents
	is_max_available_percentage_zpdb(obj)
	is_max_available_zpdb(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget should not have maxUnavailable and maxUnavailablePercentage set, %v", [display_name(obj)])
}

# Assert that maxUnavailable or maxUnavailablePercentage is set
deny contains msg if {
	obj := input[_].contents
	is_zpdb_deployment(obj)
	not is_max_available_zpdb(obj)
	not is_max_available_percentage_zpdb(obj)
    msg := sprintf("ZoneAwarePodDisruptionBudget should have maxUnavailable or maxUnavailablePercentage set, %v", [display_name(obj)])
}