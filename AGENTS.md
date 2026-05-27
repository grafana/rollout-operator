# Rollout Operator

Coordinates zone-aware rollouts and scaling of StatefulSets within a namespace, for multi-AZ deployments where each AZ is managed by a dedicated StatefulSet.

For full documentation (webhooks, ZPDB, TLS, scaling details, YAML examples), see [README.md](README.md).

## Key Concepts

- **Rollout groups**: StatefulSets with `rollout-group` label and `OnDelete` strategy are rolled one zone at a time.
- **Scaling**: Leader/follower pattern via `grafana.com/rollout-downscale-leader` annotation, or mirror-replicas from a reference resource.
- **Webhooks**: `/admission/no-downscale` (blocks downscale), `/admission/prepare-downscale` (calls prepare-shutdown endpoint before downscale).
- **ZPDB**: Custom `ZoneAwarePodDisruptionBudget` CRD evaluates eviction budgets per-zone (with optional partition awareness for ingest-storage).

## Development

```bash
make build          # Build binary
make test           # Unit tests
make build-image    # Build Docker image
make integration    # Integration tests (requires Docker + kind)

# Single integration test
make build-test-images
go test -v -tags requires_docker -timeout 30m ./integration -run TestName
```
