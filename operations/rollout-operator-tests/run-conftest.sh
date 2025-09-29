#!/bin/bash
# SPDX-License-Identifier: Apache-2.0

set -e
set -o errexit
set -o pipefail

POLICIES_PATH="operations/policies"
MANIFESTS_PATH="operations/rollout-operator-tests"

while [[ $# -gt 0 ]]; do
  case "$1" in
  --policies-path)
    POLICIES_PATH="$2"
    shift # skip --policies-path
    shift # skip policies-path value
    ;;
  --manifests-path)
    MANIFESTS_PATH="$2"
    shift # skip param name
    shift # skip param value
    ;;
  *)
    break
    ;;
  esac
done

if [ -z "$MANIFESTS_PATH" ] ; then
  echo "Provide path to manifests to test in --manifests-path"
  exit 1
fi

for FILE_PATH in "$MANIFESTS_PATH"/*.yaml; do
   TEST_NAME=$(basename -s '.yaml' "$FILE_PATH")
   echo "Testing $TEST_NAME"
   conftest test "$FILE_PATH" -p "$POLICIES_PATH" --combine
   echo ""
done
