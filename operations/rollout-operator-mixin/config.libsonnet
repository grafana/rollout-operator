{
  _config+:: {

    // set this to match a helm chart instance prefix. ie mimir
    helm: '',

    // set this to the product which the rollout-operator is supporting. ie mimir
    product: '',

    // set this to an existing dashboard uid. eg. md5(filename)
    // setting this will assert that the uid has not changed based off the generated filename
    rollout_operator_dashboard_uid: '',

    // the name for the rollout-operator. This is also used as the container name
    rollout_operator_dashboard_title: 'rollout-operator',
    rollout_operator_container_name: 'rollout-operator',
    rollout_operator_links: [],
    rollout_operator_instance_matcher:
      if $._config.helm == '' then $._config.rollout_operator_container_name + '.*' else '(.*%g-)?%g.*' % [$._config.helm, $._config.rollout_operator_container_name],

    // Shared crosshair, the crosshair will appear on all panels but the
    graph_tooltip: 1,

    // Tags for dashboards. ie 'mimir'
    tags: [],

    // The label used to differentiate between different Kubernetes clusters.
    per_cluster_label: 'cluster',
    per_namespace_label: 'namespace',
    per_job_label: 'job',

    // The label used to differentiate between different application instances (i.e. 'pod' in a kubernetes install).
    per_instance_label: 'pod',

    // The default datasource used for dashboards.
    dashboard_datasource: 'default',
    datasource_regex: '',

    // Grouping labels, to uniquely identify and group by {jobs, clusters}
    job_labels: [$._config.per_cluster_label, $._config.per_namespace_label, $._config.per_job_label],
    job_prefix: '($namespace)/',
    cluster_labels: [$._config.per_cluster_label, $._config.per_namespace_label],

    // PromQL queries used to find clusters and namespaces.
    dashboard_variables: {
      cluster_query: 'cortex_build_info',
      namespace_query: 'cortex_build_info{%s=~"$cluster"}' % $._config.per_cluster_label,
    },

    rollout_operator_resources_panel_queries: {
      cpu_usage: 'sum by(%(instanceLabel)s) (rate(container_cpu_usage_seconds_total{%(namespace)s,container=~"%(containerName)s"}[$__rate_interval]))',
      cpu_limit: 'min(container_spec_cpu_quota{%(namespace)s,container=~"%(containerName)s"} / container_spec_cpu_period{%(namespace)s,container=~"%(containerName)s"})',
      cpu_request: 'min(kube_pod_container_resource_requests{%(namespace)s,container=~"%(containerName)s",resource="cpu"})',
      memory_working_usage: 'max by(%(instanceLabel)s) (container_memory_working_set_bytes{%(namespace)s,container=~"%(containerName)s"})',
      memory_working_limit: 'min(container_spec_memory_limit_bytes{%(namespace)s,container=~"%(containerName)s"} > 0)',
      memory_working_request: 'min(kube_pod_container_resource_requests{%(namespace)s,container=~"%(containerName)s",resource="memory"})',
    },
  },
}
