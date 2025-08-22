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

You'll then be able to access the rollout operator at `http://localhost:8080`, access the rollout operator webhooks at `http://localhost:8443` and the Jaeger tracing UI at `http://localhost:16686`.

You can use the StatefulSets to exercise the operator across a multi-zone `test-app` environment.

# ZoneAwarePodDisruptionBudget (ZPDB)

Included is a `ZoneAwarePodDisruptionBudget` which can be used to enforce a multi-zone pod disruption budget.

By default, this will be applied to the `test-app` Pods and StatefulSets.

To disable this functionality from the `test-app`;

```text
kubectl delete -f rollout-operator-zone-aware-pod-disruption-budget.yaml
```

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

The following are useful commands when running tests with the `rollout-operator` and the `ZoneAwarePodDisruptionBudget`

List custom resource definitions;
```
kubectl get crds -n rollout-operator-development
```

List custom resources;
```
kubectl get zoneawarepoddisruptionbudgets -n rollout-operator-development
```

List the custom resource configuration by name;
```
kubectl get zoneawarepoddisruptionbudget test-app -n rollout-operator-development -o yaml 
```

Port forward to the rollout-operator;
```
kubectl --namespace=rollout-operator-development port-forward svc/rollout-operator 8443:443
```

Tail logs for the rollout-operator;
```
kubectl logs -f `kubectl get pods -n rollout-operator-development | grep rollout-operator | awk '{print $1}'` -n rollout-operator-development
```

Test the pod eviction and `ZPDB`;
```
# watch the pod status
while true; do kubectl get pods -n rollout-operator-development; sleep 1; clear; done
```

```
# in another shell - issue a drain for all pods
kubectl drain --pod-selector rollout-group=test-app minikube &; sleep 5; kubectl uncordon minikube
```

Note - in the above example the `uncordon` is important as without this the drained pods in the first zone will not be re-deployed. Until these pods are running again the next zone's pods will not be evicted.

This is a limitation of running multiple zones within a single kubernetes node. In a usual deployment each zone would have its pods distributed across different nodes.

Apply an updated `ZPDB`
```
# make changes in rollout-operator-zone-aware-pod-disruption-budget.yaml
kubectl apply -f rollout-operator-zone-aware-pod-disruption-budget.yaml
```

Test the pod eviction webhook manually;
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
