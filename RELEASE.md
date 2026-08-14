# How to release a new version

1. Update `CHANGELOG.md`
  - Open PR and get it merged
2. Create a new tag that follows semantic versioning:
    ```bash
    $ tag=v0.1.0
    $ git tag -s "${tag}" -m "${tag}"
    $ git push origin "${tag}"
    ```
3. The CI workflow will run automatically. It will build and push the images to Docker Hub, and create a GitHub release with release notes.
4. Update the Helm Chart
  - Repository https://github.com/grafana/helm-charts/tree/main/charts/rollout-operator
  - [Example PR](https://github.com/grafana/helm-charts/pull/3177/files)
