# Quick Start

This directory contains Kubernetes manifests to start an instance of the `rollout-operator` locally.

To use it:

* Build the `rollout-operator` image: `make build-image`
* Make the image available to your Kubernetes cluster (not required for use with Docker Desktop)
* Apply the Kubernetes manifests: `./apply.sh`
* Port forward to the operator service;
```
kubectl --namespace=rollout-operator-development port-forward svc/rollout-operator 8080:80
kubectl --namespace=rollout-operator-development port-forward svc/rollout-operator 8443:443
```
* Port forward to the Jaeger UI: `kubectl --namespace=rollout-operator-development port-forward svc/jaeger 16686:16686`

You'll then be able to access the rollout operator at `http://localhost:8080`, and the Jaeger tracing UI at `http://localhost:16686`.

You can use the StatefulSets to exercise the operator across a multi-zone `test-app` environment.

# PodDisruptionZoneBudget (PDZB)

Supplied in this directory are manifest files for enabling the `PDZB`. This includes;

* a custom resource definition for the `PodDisruptionZoneBudget` kind
* a custom resource of a `PodDisruptionZoneBudget` kind for use with the `test-app` rollout-group
* a `ValidatingWebhookConfiguration` kind for registering the rollout-operator for pod evictions 

Additionally, the rollout-operator Role has been modified to grant the following;

```text
- apiGroups:
      - rollout-operator.grafana.com
    resources:
      - poddisruptionzonebudgets
    verbs:
      - get
      - list
      - watch
```

This custom resource definition defines a namespace scoped resource which can be used to set configuration for the zone aware pod disruption budget eviction handler.

When used in conjunction with the pod eviction `ValidatingWebhookConfiguration`, a pod eviction webhook is registered for approving voluntary pod eviction requests.

Unlike a regular `PodDisruptionBudget` which evaluates across all pods within it's scope, the `PodDisruptionZoneBudget` evaluates against unavailable pod counts against other zones.

Consider the following topology where the `PDZB` has `maxUnavailability=1`

* StatefulSet `ingester-zone-a` manages pods `ingester-zone-a-0` and `ingester-zone-a-1`
* StatefulSet `ingester-zone-b` manages pods `ingester-zone-b-0` and `ingester-zone-b-1`
* StatefulSet `ingester-zone-c` manages pods `ingester-zone-c-0` and `ingester-zone-c-1`

When a pod eviction request is received, the availability of the pods in the `other` zones are considered.

If `ingester-zone-a-0` is to be evicted, it will be allowed if there are no disruptions in either zone b or zone c. 

If `ingester-zone-a-1` has failed and `ingester-zone-a-0` is to be evicted, it will be allowed if there are no disruptions in either zone b or zone c. 

*The already disrupted zone can be further disrupted as long as the other zones meet the unavailability criteria.*

If `ingester-zone-a-0` is to be evicted, and `ingester-zone-b-0` has failed, the eviction request will be denied. 

*A pod eviction is only allowed if the number of unavailable pods within each other zone is less than the maxUnavailability value.*

## PDZB Partitions

The `PodDisruptionZoneBudget` can be extended to limit the scope of the availability check to just a specific partition. 

*A pod eviction is only allowed if the number of unavailable pods serving a specific partition within each other zone is less than the maxUnavailability value.*

If `ingester-zone-b-0` has failed and `ingester-zone-a-1` is to be evicted, it will be allowed if there are no disruptions in either zone b or zone c for partition `1`.

If `ingester-zone-b-0` has failed and `ingester-zone-a-0` is to be evicted, it will be denied as the partition `0` in zone b is disrupted.

## PDZB Configuration

The exact resource attributes should be referenced via the provided custom resource definition file. 

Included in the configuration options is the ability to;

* set a fixed max unavailable threshold
* set a percentage of unavailable StatefulSet members as the threshold
* set the selector to match the StatefulSets across the zones
* set the regular expression to determine a partition from a pod name

*Note - the max unavailable can be set to 0. In this case no voluntary evictions in any zone will be allowed.*

# Minikube & Docker Desktop

Note - if you are using local `Docker Desktop` and `minikube` and you intend to use locally built images, ensure that you are using Minikube's Docker Daemon so any images you build will be available inside the Minikube cluster.

Additionally, ensure that you set the container image reference to `imagePullPolicy: Never`.

```
cd ~/rollout-operator
minikube start
eval $(minikube docker-env)
make build-image

(
    cd development
    ./apply.sh
)
```

# Useful commands

The following are useful commands when running tests with the `rollout-operator` and the `PodDisruptionZoneBudget`

List custom resource definitions;
```
kubectl get crds -n rollout-operator-development
```

List custom resources;
```
kubectl get poddisruptionzonebudgets.rollout-operator.grafana.com -n rollout-operator-development
kubectl get poddisruptionzonebudgets -n rollout-operator-development
```

List the custom resource configuration by name;
```
get poddisruptionzonebudget test-app -n rollout-operator-development -o yaml 
```

List all resources in namespace;
```
get all -n rollout-operator-development
```

Port forward to the rollout-operator;
```
kubectl --namespace=rollout-operator-development port-forward svc/rollout-operator 8443:443
```

Tail logs for the rollout-operator;
```
kubectl logs -f `kubectl get pods -n rollout-operator-development | grep rollout-operator | awk '{print $1}'` -n rollout-operator-development
```

Test a pod eviction;
```
curl --insecure -X POST "https://127.0.0.1:8443/admission/pod-eviction" \
  -H "Content-Type: application/json" \
  --data '{
    "apiVersion": "admission.k8s.io/v1",
    "kind": "AdmissionReview",
    "request": {
      "uid": "test-eviction-123",
      "kind": {"group": "policy", "version": "v1", "kind": "Eviction"},
      "resource": {"group": "policy", "version": "v1", "resource": "evictions"},
      "name": "test-app-zone-a-0",
      "namespace": "rollout-operator-development",
      "operation": "CREATE",
      "subResource": "eviction",
      "userInfo": {"username": "test-user"},
      "dryRun": false,
      "object": {
        "apiVersion": "policy/v1",
        "kind": "Eviction",
        "metadata": {"name": "test-app-zone-a-0", "namespace": "rollout-operator-development"},
        "deleteOptions": {"gracePeriodSeconds": 30}
      }
    }
  }'
```

Test a denied pod eviction;
```
kubectl delete pod test-app-zone-b-0 -n rollout-operator-development; curl <...as above...>
```

