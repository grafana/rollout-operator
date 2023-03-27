#!/usr/bin/env bash

# SPDX-License-Identifier: AGPL-3.0-only

set -eo pipefail

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")
CODEGEN_PKG=$(cd "${SCRIPT_ROOT}/.." && ls -d -1 ./vendor/k8s.io/code-generator)

# generate the code with -output-base to make sure it ends up in the current working directory
bash "${CODEGEN_PKG}/generate-groups.sh" "deepcopy,client,informer,lister" \
  github.com/grafana/rollout-operator/pkg/generated \
  github.com/grafana/rollout-operator/pkg/apis \
  rolloutoperator:v1alpha1 \
  --output-base "$SCRIPT_ROOT" \
  --go-header-file "${SCRIPT_ROOT}"/boilerplate.go.txt

rm -rf ${SCRIPT_ROOT}/../pkg/generated
mv ${SCRIPT_ROOT}/github.com/grafana/rollout-operator/pkg/generated ${SCRIPT_ROOT}/../pkg/generated
mv ${SCRIPT_ROOT}/github.com/grafana/rollout-operator/pkg/apis/rolloutoperator/v1alpha1/zz_generated.deepcopy.go ${SCRIPT_ROOT}/../pkg/apis/rolloutoperator/v1alpha1/
rm -r ${SCRIPT_ROOT}/github.com/
