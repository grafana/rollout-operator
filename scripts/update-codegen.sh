#!/usr/bin/env bash

# SPDX-License-Identifier: AGPL-3.0-only

set -eo pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")
CODEGEN_PKG=$(cd "${SCRIPT_ROOT}/.." && ls -d -1 ./vendor/k8s.io/code-generator)

# generate the code with:
# --output-base    because this script should also be able to run inside the vendor dir of
#                  k8s.io/kubernetes. The output-base is needed for the generators to output into the vendor dir
#                  instead of the $GOPATH directly. For normal projects this can be dropped.
"${CODEGEN_PKG}/generate-groups.sh" "deepcopy,client,informer,lister" \
  github.com/grafana/rollout-operator/pkg/generated \
  github.com/grafana/rollout-operator/pkg/apis \
  samplecontroller:v1alpha1 \
  --output-base "$SCRIPT_ROOT/.." \
  --go-header-file "${SCRIPT_ROOT}"/../boilerplate.go.txt

# To use your own boilerplate text append:
#   --go-header-file "${SCRIPT_ROOT}"/hack/custom-boilerplate.go.txt
