# How to release a new version

1. Open and merge a PR
   - Update `CHANGELOG.md` as required 
   - Update [images.libsonnet](operations/rollout-operator/images.libsonnet) - set the version based off what the following tag will be
   - Run `make build-jsonnet-tests` to update the jsonnet tests to include the new image version

2. Create a new tag that follows semantic versioning:
    ```bash
    $ tag=v0.1.0
    $ git tag -s "${tag}" -m "${tag}"
    $ git push origin "${tag}"
    ```

3. The [CI workflow](.github/workflows/ci.yaml) will run automatically.
   It will build and push the image to [Docker Hub](https://hub.docker.com/r/grafana/rollout-operator), and create a [GitHub release](https://github.com/grafana/rollout-operator/releases) with release notes.

4. Update the rollout-operator Helm Chart:
   - Repository https://github.com/grafana/helm-charts/tree/main/charts/rollout-operator
   - [Example PR](https://github.com/grafana/helm-charts/pull/3177/files)

5. Update the mimir-distributed Helm Chart:
   - Repository https://github.com/grafana/mimir/tree/main/operations/helm/charts/mimir-distributed/
   - [Example PR](https://github.com/grafana/mimir/pull/12591/files)
