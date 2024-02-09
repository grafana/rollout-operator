# Diagram of how rollout-operator performs scaling based on custom resource.

Scaling based on reference resource, without delayed downscale:

```mermaid
sequenceDiagram
    participant HPA as HorizontalPodAutoscaler
    participant CS as Custom Resource
    participant RO as rollout-operator
    participant SS as StatefulSet
    HPA->>CS: Updates "scale" subresource with desired number of replicas
    loop Rollout-Operator Reconciliation Loop
        RO->>CS: Reads "scale" subresource for replicas
        RO->>SS: If scale replicas and StatefulSet spec.replicas differ, updates spec.replicas
        RO->>CS: Updates replicas in "status" subresource with current number of replicas
    end
```

Scaling based on reference resource, with delayed downscale:

```mermaid
sequenceDiagram
    participant HPA as HorizontalPodAutoscaler
    participant CS as Custom Resource
    participant RO as rollout-operator
    participant SS as StatefulSet
    participant Pods
    
    HPA->>CS: Updates "scale" subresource with desired number of replicas
    loop Rollout-Operator Reconciliation Loop
        RO->>CS: Reads "scale" subresource for replicas
        alt is downscale
            RO->>Pods: calls POST prepare-delayed-downscale-url on all downscaled pods
            RO->>Pods: calls DELETE prepare-delayed-downscale-url on all non-downscaled pods
            RO->>SS: If delay has elapsed, updates spec.replicas
        else is upscale, or no change in replicas.
            RO->>Pods: calls DELETE prepare-delayed-downscale-url on all pods
            RO->>SS: If scale replicas and StatefulSet spec.replicas differ,<br />updates spec.replicas
        end
        RO->>CS: Updates replicas in "status" subresource with current number of replicas
    end
```
