package main

# Tests which relate to the expected Deployment manifests

port_match(deploymentPort, servicePort) if {
    object.get(deploymentPort, "containerPort", 0) == object.get(servicePort, "targetPort", 0)
    object.get(deploymentPort, "name", 0) == object.get(servicePort, "name", 0)
}

# Assert that the Deployment and Service have matching https port definitions
deny contains msg if {
    has_webhooks_deployment

	obj := input[_].contents
	is_deployment(obj)

	svc := input[_].contents
	is_service(svc)

    deploymentPort := obj.spec.template.spec.containers[_].ports[_]
    deploymentPort.name != "http-metrics"

    servicePort := svc.spec.ports[_]
    servicePort.name == "https"

	not port_match(deploymentPort, servicePort)
    msg := sprintf("Deployment webhooks container port does not match service port, %v. Deployment has %v, Service has %v", [display_name(obj), deploymentPort, servicePort])
}

deny contains msg if {
    deployment := input[_].contents
    is_deployment(deployment)
    deployment.spec.strategy.rollingUpdate.maxUnavailable != 1
    msg := sprintf("Deployment has unexpected rollingUpdate.maxUnavailable, %v. Expected 1, got %v", [display_name(deployment), deployment.spec.strategy.rollingUpdate.maxUnavailable])
}

deny contains msg if {
    deployment := input[_].contents
    is_deployment(deployment)
    deployment.spec.strategy.rollingUpdate.maxSurge != 0
    msg := sprintf("Deployment has unexpected rollingUpdate.maxSurge, %v. Expected 0, got %v", [display_name(deployment), deployment.spec.strategy.rollingUpdate.maxSurge])
}


# Assert that the deployment spec serviceAccountName matches the ServiceAccount name
deny contains msg if {
    deployment := input[_].contents
    is_deployment(deployment)
    account := input[_].contents
    is_service_account(account)
    account.metadata.name != deployment.spec.template.spec.serviceAccountName
    msg := sprintf("Deployment template spec serviceAccountName does not match ServiceName, %v. Got %v, expected %v", [display_name(deployment), deployment.spec.template.spec.serviceAccountName, account.metadata.name])
}

# Assert that the deployment spec name will match the service selector
deny contains msg if {
    deployment := input[_].contents
    is_deployment(deployment)
    service := input[_].contents
    is_service(service)
    service.spec.selector.name != deployment.spec.template.spec.containers[0].name
    msg := sprintf("Deployment template spec container name does not match Service selector, %v. Got %v, expected %v", [display_name(deployment), deployment.spec.template.spec.containers[0].name, service.spec.selector.name])
}