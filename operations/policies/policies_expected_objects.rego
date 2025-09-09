package main

# Assert that we have the expected manifest files in each configuration

deny contains msg if {
    not has_service_account
    msg := "Missing service account"
}

deny contains msg if {
    not has_role
    msg := "Missing role"
}

deny contains msg if {
    not has_role_binding
    msg := "Missing role binding"
}

deny contains msg if {
    not has_deployment
    msg := "Missing deployment"
}

deny contains msg if {
    has_webhooks_deployment
    not has_service
    msg := "Missing service"
}

deny contains msg if {
    has_webhooks_deployment
    not has_cluster_role
    msg := "Missing cluster role"
}

deny contains msg if {
    has_webhooks_deployment
    not has_cluster_role_binding
    msg := "Missing cluster role binding"
}

deny contains msg if {
    has_webhooks_deployment
    not has_webhooks_cert_role
    msg := "Missing webhooks certificate role"
}


deny contains msg if {
    has_webhooks_deployment
    not has_webhooks_cert_role_binding
    msg := "Missing webhooks certificate role binding"
}

deny contains msg if {
    has_webhooks_deployment
    not has_prepare_downscale_webhook
    msg := "Missing prepare_downscale webhook"
}

deny contains msg if {
    has_webhooks_deployment
    not has_no_downscale_webhook
    msg := "Missing no_downscale webhook"
}

deny contains msg if {
    has_webhooks_deployment
    not has_pod_eviction_webhook
    msg := "Missing pod_eviction webhook"
}

deny contains msg if {
    has_webhooks_deployment
    not has_zpdb_validation_webhook
    msg := "Missing zpdb_valildation webhook"
}

deny contains msg if {
    has_replica_template_deployment
    not has_webhooks_deployment
    msg := "A ReplicaTemplate configuration requires webhooks to be enabled"
}

deny contains msg if {
    has_zpdb_deployment
    not has_webhooks_deployment
    msg := "A ZoneAwarePodDisruptionBudget configuration requires webhooks to be enabled"
}