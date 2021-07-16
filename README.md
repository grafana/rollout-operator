# Kubernetes Rollout Operator

This operator coordinates the rollout of pods between different StatefulSets within a specific namespace and can be used to manage multi-AZ deployments where pods running in each AZ are managed by a dedicated StatefulSet.

## How it works

The operator coordinates the rollout of pods belonging to `StatefulSets` with the `rollout-group` label and updates strategy set to `OnDelete`. The label value should identify the group of StatefulSets to which the StatefulSet belongs to.

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

### RBAC

When running the `rollout-operator` as a pod, it needs a Role with at least the following privileges:

```
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
