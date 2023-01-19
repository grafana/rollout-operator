# Kubernetes Rollout Operator

This operator coordinates the rollout of pods between different StatefulSets within a specific namespace and can be used to manage multi-AZ deployments where pods running in each AZ are managed by a dedicated StatefulSet.

## How it works

The operator coordinates the rollout of pods belonging to `StatefulSets` with the `rollout-group` label and updates strategy set to `OnDelete`. The label value should identify the group of StatefulSets to which the StatefulSet belongs to. Make sure the statefulset has a label `name` in its `spec.template`, the operator uses it to find pods belonging to it.

For example, given the following StatefulSets in a namespace:
- `ingester-zone-a` with `rollout-group: ingester`
- `ingester-zone-b` with `rollout-group: ingester`
- `compactor-zone-a` with `rollout-group: compactor`
- `compactor-zone-b` with `rollout-group: compactor`

The operator independently coordinates the rollout of pods of each group:
- Rollout group: `ingester`
  - `ingester-zone-a`
  - `ingester-zone-b`
- Rollout group: `compactor`
  - `compactor-zone-a`
  - `compactor-zone-b`

For each **rollout group**, the operator **guarantees**:
1. Pods in 2 different StatefulSets are not rolled out at the same time
1. Pods in a StatefulSet are rolled out if and only if all pods in all other StatefulSets of the same group are `Ready` (otherwise it will start or continue the rollout once this check is satisfied)
1. Pods are rolled out if and only if all StatefulSets in the same group have `OnDelete` update strategy (otherwise the operator will skip the group and log an error)
1. The maximum number of not-Ready pods in a StatefulSet doesn't exceed the value configured in the `rollout-max-unavailable` annotation (if not set, it defaults to `1`). Values:
  - `<= 0`: invalid (will default to `1` and log a warning)
  - `1`: pods are rolled out sequentially
  - `> 1`: pods are rolled out parallelly (honoring the configured number of max unavailable pods)

## Operations

### HTTP endpoints

The operator runs an HTTP server listening on port `-server.port=8001` exposing the following endpoints.

#### `/ready`

Readiness probe endpoint.

#### `/metrics`

Prometheus metrics endpoint.

### HTTPS endpoints

#### `/admission/no-downscale`

Offers a `ValidatingAdmissionWebhook` that rejects the requests that decrease the number of replicas in objects labeled as `grafana.com/no-downscale: true`. See [Webhooks](#webhooks) section below. 

### RBAC

When running the `rollout-operator` as a pod, it needs a Role with at least the following privileges:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
rules:
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - list
  - get
  - watch
  - delete
- apiGroups:
  - apps
  resources:
  - statefulsets
  verbs:
  - list
  - get
  - watch
- apiGroups:
  - apps
  resources:
  - statefulsets/status
  verbs:
  - update
```

(Please see [Webhooks](#webhooks) section below for extra roles required when using the HTTPS server for webhooks.)

## Webhooks

[Dynamic Admission Control](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/) webhooks are offered on the HTTPS server of the rollout-operator.
You can enable HTTPS by setting the flag `-server-tls.enabled=true`.
The HTTPS server will listen on port `-server-tls.port=8443` and expose the following endpoints.

### `/admission/no-downscale`

This webhook offers a `ValidatingAdmissionWebhook` that rejects the requests that decrease the number of replicas in objects labeled as `grafana.com/no-downscale: true`.
An example webhook configuration would look like this:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  labels:
    grafana.com/inject-rollout-operator-ca: "true"
    grafana.com/namespace: default
  name: no-downscale-default
webhooks:
- name: no-downscale-default.grafana.com
  admissionReviewVersions: [v1]
  clientConfig:
    service:
      name: rollout-operator
      namespace: default
      path: /admission/no-downscale
      port: 443
  failurePolicy: Fail
  matchPolicy: Equivalent
  rules:
  - apiGroups: [apps]
    apiVersions: [v1]
    operations: [UPDATE]
    resources:
    - statefulsets
    - deployments
    - replicasets
    - statefulsets/scale
    - deployments/scale
    - replicasets/scale
    scope: Namespaced
  sideEffects: None
  timeoutSeconds: 10
```

This webhook configuration should point to a `Service` that points to the `rollout-operator`'s HTTPS server exposed on port `-server-tls.port=8443`. 
For example:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: rollout-operator
spec:
  selector:
    name: rollout-operator
  type: ClusterIP
  ports:
  - name: https
    port: 443
    protocol: TCP
    targetPort: 8443
```

#### No-downscale webhook details

##### Matching objects

Please note that the webhook will NOT receive the requests for `/scale` operations [if an `objectSelector` is provided](https://github.com/kubernetes/kubernetes/issues/113594).
For this reason the webhook will perform the check of the `grafana.com/no-downscale` label on the object itself on every received request. 
When an object like `StatefulSet`, `DeploymentSet` or `ReplicaSet` is changed itself, the validation request will include the changed object and the webhook will be able to check the label on it. 
When a `/scale` subresouce is changed (for example by running `kubectl scale ...`) the request will not contain the changed object, and `rollout-operator` will use the Kubernetes API to retrieve the parent object and check the label on it.
 
You will see in [TLS Certificates](#tls-certificates) section below that this label is also used to inject the CA bundle into the webhook configuration.

 > *Note*: if you plan running validations on `DeploymentSet` or `ReplicaSet` objects, you need to make sure that the `rollout-operator` has the privileges to list and get those objects.

##### Matching namespaces

Since the `ValidatingAdmissionWebhook` is cluster-wide, it's a good idea to at least include a `namespaceSelector` in the webhook configuration to limit the scope of the webhook to a specific namespace.
If you want to restrict the webhook to a specific namespace, you can use the `namespaceSelector` in the webhook configuration and match on the `kubernetes.io/metadata.name` label, [which contains the namespace name](https://kubernetes.io/docs/reference/labels-annotations-taints/#kubernetes-io-metadata-name).

##### Handling errors

The webhook is conservative and allows changes whenever an error occurs:
- When parent object can't be retrieved from the API.
- When the validation request can't be decoded or includes an unsupported type.

##### Special cases

Changing the replicas number to `null` (or from `null`) is allowed.

### TLS Certificates

Note that `ValidatingAdmissionWebhook` require a TLS connection, so the HTTPS server should either use a certificate signed by a well-known CA or a self-signed certificate.
You can either [issue a Certificate Signing Request](https://kubernetes.io/docs/reference/access-authn-authz/certificate-signing-requests/) or use an existing approach for issuing self-signed certificates, like [cert-manager](https://cert-manager.io/docs/configuration/selfsigned/).
You can set the options `-server-tls.cert-file` and `-server-tls.key-file` to point to the certificate and key files respectively.

For convenience, `rollout-operator` offers a self-signed certificates generator that is enabled by default.
This generator will generate a self-signed certificate and store it in a secret specified by the flag `-server-tls.self-signed-cert.secret-name`.
The certificate is stored in a secret in order to reuse it across restarts of the `rollout-operator`.

`rollout-operator` will list all the `ValidatingWebhookConfiguration` objects in the cluster that are labeled with `grafana.com/inject-rollout-operator-ca: true` and `grafana.com/namespace: <value of -kubernetes-namespace>` and will inject the CA certificate in the `caBundle` field of the webhook configuration. 
This mechanism can be disabled by setting `-webhooks.update-ca-bundle=false`.

This signing and injecting is performed at service startup once, so you would need to restart `rollout-operator` if you want to inject the CA certificate in a new `ValidatingWebhookConfiguration` object.

In order to perform the self-signed certificate generation, `rollout-operator` needs a `Role` that would allow it to list and update the secrets, as well as a `ClusterRole` that would allow listing and patching the `ValidationWebhookConfigurations`. An example of those could be:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: rollout-operator-webhook-role
  namespace: default
rules:
  - apiGroups: [""]
    resources: [secrets]
    verbs: [create]
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [rollout-operator-self-signed-certificate]
    verbs: [update, get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: rollout-operator-webhook-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: rollout-operator-webhook-role
subjects:
  - kind: ServiceAccount
    name: rollout-operator
    namespace: default
```

And:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: rollout-operator-webhook-default-clusterrole
rules:
- apiGroups: [admissionregistration.k8s.io]
  resources: [validatingwebhookconfigurations]
  verbs: [list, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: rollout-operator-webhook-default-clusterrolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: rollout-operator-webhook-default-clusterrole
subjects:
  - kind: ServiceAccount
    name: rollout-operator
    namespace: default
```

#### Certificate expiration

Whenever the certificate expires, the `rollout-operator` will detect it and will restart, which will trigger the self-signed certificate generation again if it's configured.
The default expiration for the self-signed certificate is 1 year and it can be changed by setting the flag `-server-tls.self-signed-cert.expiration`.

