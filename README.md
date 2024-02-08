# Kubernetes Rollout Operator

This operator coordinates the rollout of pods between different StatefulSets within a specific namespace and can be used to manage multi-AZ deployments where pods running in each AZ are managed by a dedicated StatefulSet.

## How updates work

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
  - `> 1`: pods are rolled out in parallel (honoring the configured number of max unavailable pods)

## How scaling up and down works

The operator can also optionally coordinate scaling up and down of `StatefulSets` that are part of the same `rollout-group` based on the `grafana.com/rollout-downscale-leader` annotation. When using this feature, the `grafana.com/min-time-between-zones-downscale` label must also be set on each `StatefulSet`.

This can be useful for automating the tedious scaling of stateful services like Mimir ingesters. Making use of this feature requires adding a few annotations and labels to configure how it works. Examples for a multi-AZ ingester group are given below.

- For `ingester-zone-a`, add the following:
  - Labels:
    - `grafana.com/min-time-between-zones-downscale=12h` (change the value here to an appropriate duration)
    - `grafana.com/prepare-downscale=true` (to allow the service to be notified when it will be scaled down)
  - Annotations:
    - `grafana.com/prepare-downscale-http-path=ingester/prepare-shutdown` (to call a specific endpoint on the service)
    - `grafana.com/prepare-downscale-http-port=80` (to call a specific endpoint on the service)
- For `ingester-zone-b`, add the following:
  - Labels:
    - `grafana.com/min-time-between-zones-downscale=12h` (change the value here to an appropriate duration)
    - `grafana.com/prepare-downscale=true` (to allow the service to be notified when it will be scaled down)
  - Annotations:
    - `grafana.com/rollout-downscale-leader=ingester-zone-a` (zone `b` will follow zone `a`, after a delay) 
    - `grafana.com/prepare-downscale-http-path=ingester/prepare-shutdown` (to call a specific endpoint on the service)
    - `grafana.com/prepare-downscale-http-port=80` (to call a specific endpoint on the service)
- For `ingester-zone-c`, add the following:
  - Labels:
    - `grafana.com/min-time-between-zones-downscale=12h` (change the value here to an appropriate duration)
    - `grafana.com/prepare-downscale=true` (to allow the service to be notified when it will be scaled down)
  - Annotations:
    - `grafana.com/rollout-downscale-leader=ingester-zone-b` (zone `c` will follow zone `b`, after a delay)
    - `grafana.com/prepare-downscale-http-path=ingester/prepare-shutdown` (to call a specific endpoint on the service)
    - `grafana.com/prepare-downscale-http-port=80` (to call a specific endpoint on the service)

## Scaling based on reference resource

Rollout-operator can use custom resource with `scale` and `status` subresources as a "source of truth" for number of replicas for target statefulset. "Source of truth" resource (or "reference resource") is configured using following annotations:

* `grafana.com/rollout-mirror-replicas-from-resource-name`
* `grafana.com/rollout-mirror-replicas-from-resource-kind`
* `grafana.com/rollout-mirror-replicas-from-resource-api-version`

These annotations must be set on StatefulSet that rollout-operator will scale (ie. target statefulset). Number of replicas in target statefulset will follow replicas in reference resource (from `scale` subresource), while reference resource's `status` subresource will be updated with current number of replicas in target statefulset.

This is similar to using `grafana.com/rollout-downscale-leader`, but reference resource can be any kind of resource, not just statefulset. Furthermore `grafana.com/min-time-between-zones-downscale` is not respected when using scaling based on reference resource.

This can be used in combination with HorizontalPodAutoscaler, when it is undesireable to set number of replicas directly on target statefulset, because we want to add custom logic to the scaledown (see next point). In that case, HPA can update different "reference resource", and rollout-operator can "mirror" number of replicas from reference resource to target statefulset.

To support scaling based on reference resource, rollout-operator needs to be allowed to execute `get` and `patch` verbs on `status` and `scale` subresources of the custom resource. For example when using custom resource `replica-templates` from API group `rollout-operator.grafana.com`, you can add following to the RBAC:

```yaml
- apiGroups:
  - rollout-operator.grafana.com
  resources:
  - replica-templates/scale
  - replica-templates/status
  verbs:
  - get
  - patch
```

## Delayed scaledown

When using "Scaling based on reference resource", rollout-operator can be configured to delay the actual scaledown, and ask individual pods to prepare for delayed-scaledown.

This is configured using `grafana.com/rollout-delayed-downscale` and `grafana.com/rollout-prepare-delayed-downscale-url` annotations on target statefulset. First annotation specificies minimum delay duration between call to "prepare-delayed-downscale-url" and actual scaledown, while the second annotation specifies the URL that is called on each pod. (URL is used as-is, but host is replaced with pod's fully qualified domain name.)

Rollout operator has special requirements on the configured endpoint:
* Endpoint must support `POST` and `DELETE` methods.
* On `POST` method, pod is supposed to prepare for delayed downscale. Endpoint must also return 200 if preparation succeeded, and JSON body in format: `{"timestamp": 123456789}`, where timestamp is Unix timestamp in seconds when the preparation has been done.
* Repeated calls with `POST` method should return the same timestamp, unless preparation was done again, and new waiting must start.
* On `DELETE` method, pod should cancel the preparation for delayed downscale. If there's nothing to do, pod should ignore such `DELETE` request.  

Rollout-operator does NOT remember any state of "delayed scaledown" preparation. It relies on timestamps returned from the pod endpoints on `POST` method. When no delayed scaledown is taking place, rollout-operator still keeps calling `DELETE` method regularly, to make sure that there is all pods have cancelled any previous "preparation of delayed scaledown".

How is this different from `grafana.com/prepare-downscale` label used by `/admission/prepare-downscale` webhook? That webhook calls the "prepare-downscale" endpoint called *just* before the downscale is done, and pods are shutdown right after. On the other hand delayed downscale can take many hours. Delayed downscale and "prepare downscale" features can be used together.

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

### `/admission/prepare-downscale`

This webhook offers a `MutatingAdmissionWebhook` that calls a downscale preparation endpoint on the pods for requests that decrease the number of replicas in objects labeled as `grafana.com/prepare-downscale: true`.
An example webhook configuration would look like this:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  labels:
    grafana.com/inject-rollout-operator-ca: "true"
    grafana.com/namespace: default
  name: prepare-downscale-default
webhooks:
- admissionReviewVersions:
  - v1
  clientConfig:
    service:
      name: rollout-operator
      namespace: default
      path: /admission/prepare-downscale
      port: 443
  failurePolicy: Fail
  matchPolicy: Equivalent
  name: prepare-downscale-default.grafana.com
  rules:
  - apiGroups:
    - apps
    apiVersions:
    - v1
    operations:
    - UPDATE
    resources:
    - statefulsets
    - statefulsets/scale
    scope: Namespaced
  sideEffects: NoneOnDryRun
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

Note that the `Service` created for the `/admission/no-downscale` can be reused if already present.

#### Prepare-downscale webhook details

Upscaling requests or requests that don't change the number of replicas are approved.
For downscaling requests the following labels have to be present on the object:

- `grafana.com/prepare-downscale`

The following annotations also have to be present:

- `grafana.com/prepare-downscale-http-path`
- `grafana.com/prepare-downscale-http-port`

If the `grafana.com/last-downscale` annotation is present on any of the stateful sets in the same rollout group it's value will be checked against the current time. If the difference is less than the `grafana.com/min-time-between-zones-downscale` label (if present) then the request is rejected. Otherwise the request is approved. This mechanism can be used to maintain a time between downscales of the stateful sets in a rollout group.

The endpoint created from `grafana.com/prepare-downscale-http-path` and `grafana.com/prepare-downscale-http-port` will be called for each of the pods that have to be downscaled. If any of these requests fail the downscaling request is rejected.

The `grafana.com/last-downscale` annotation is added to the stateful set mentioned in the validation request.

### TLS Certificates

Note that both the `ValidatingAdmissionWebhook` and the `MutatingAdmissionWebhook` require a TLS connection, so the HTTPS server should either use a certificate signed by a well-known CA or a self-signed certificate.
You can either [issue a Certificate Signing Request](https://kubernetes.io/docs/reference/access-authn-authz/certificate-signing-requests/) or use an existing approach for issuing self-signed certificates, like [cert-manager](https://cert-manager.io/docs/configuration/selfsigned/).
You can set the options `-server-tls.cert-file` and `-server-tls.key-file` to point to the certificate and key files respectively.

For convenience, `rollout-operator` offers a self-signed certificates generator that is enabled by default.
This generator will generate a self-signed certificate and store it in a secret specified by the flag `-server-tls.self-signed-cert.secret-name`.
The certificate is stored in a secret in order to reuse it across restarts of the `rollout-operator`.

`rollout-operator` will list all the `ValidatingWebhookConfiguration` or `MutatingWebhookConfiguration` objects in the cluster that are labeled with `grafana.com/inject-rollout-operator-ca: true` and `grafana.com/namespace: <value of -kubernetes-namespace>` and will inject the CA certificate in the `caBundle` field of the webhook configuration. 
This mechanism can be disabled by setting `-webhooks.update-ca-bundle=false`.

This signing and injecting is performed at service startup once, so you would need to restart `rollout-operator` if you want to inject the CA certificate in a new `ValidatingWebhookConfiguration` object.

In order to perform the self-signed certificate generation, `rollout-operator` needs a `Role` that would allow it to list and update the secrets, as well as a `ClusterRole` that would allow listing and patching the `ValidationWebhookConfigurations` or `MutatingWebhookConfigurations`. An example of those could be:

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
