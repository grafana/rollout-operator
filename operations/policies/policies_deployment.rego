package main

# Tests which relate to the expected Deployment manifests


is_rollout_operator_deployment(obj) if {
    obj.kind == "Deployment"
    obj.metadata.name == "rollout-operator"
}

is_rollout_operator_arg_found(args, arg) if {
   some i
   a := args[i]
   a == arg
}

is_rollout_operator_deployment_valid(obj) if {
  obj.spec.replicas == 1
  obj.spec.selector.matchLabels.name == "rollout-operator"
  obj.spec.strategy.rollingUpdate.maxUnavailable == 1
  obj.spec.strategy.rollingUpdate.maxSurge == 0
  is_rollout_operator_arg_found(obj.spec.template.spec.containers[0].args, "-use-zone-tracker=true")
  is_rollout_operator_arg_found(obj.spec.template.spec.containers[0].args, "-zone-tracker.config-map-name=rollout-operator-zone-tracker")
  obj.spec.template.spec.containers[0].ports[0].name == "http-metrics"
  obj.spec.template.spec.containers[0].ports[0].containerPort == 8001
  obj.spec.template.spec.serviceAccountName == "rollout-operator"
  obj.spec.template.spec.containers[0].imagePullPolicy == "IfNotPresent"
  startswith(obj.spec.template.spec.containers[0].image, "grafana/rollout-operator:")
}

is_rollout_operator_deployment_valid_wh(obj, isWebhooksDeployment) if {
    is_rollout_operator_deployment_valid(obj)
    not isWebhooksDeployment
}

is_rollout_operator_deployment_valid_wh(obj, true) if {
    is_rollout_operator_deployment_valid(obj)
    obj.spec.template.spec.containers[0].ports[1].name == "https"
    obj.spec.template.spec.containers[0].ports[1].containerPort == 8443
}

# Assert that the Deployment has expected attributes
deny contains msg if {
	obj := input[i].contents
	wh := is_webhooks_deployment
	is_rollout_operator_deployment(obj)
	not is_rollout_operator_deployment_valid_wh(obj, wh)
    msg := sprintf("Deployment does not have expected attributes, %v %v", [object_display_name[i], obj])
}
