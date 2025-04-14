# How to release a new version

1. Update `CHANGELOG.md`
  - Open PR and get it merged
2. Create a new tag that follows semantic versioning:
    ```bash
    $ tag=v0.1.0
    $ git tag -s "${tag}" -m "${tag}"
    $ git push origin "${tag}"
    ```
3. Publish the updated Docker image
    ```bash
    $ IMAGE_TAG="${tag}" make publish-images
    ```
4. Create a new GitHub release based on the tag. The release notes can be generated with:
    ```bash
    $ echo "Docker images: \`grafana/rollout-operator:${tag}\` and \`grafana/rollout-operator-boringcrypto:${tag}\`\n\n## Changelog" && awk -v var="${tag}" '$0 ~ "## "var {flag=1; next} /^##/{flag=0} flag' CHANGELOG.md
    ```
5. Update the Helm Chart
  - Repository https://github.com/grafana/helm-charts/tree/main/charts/rollout-operator
  - [Example PR](https://github.com/grafana/helm-charts/pull/3177/files)
