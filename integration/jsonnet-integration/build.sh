#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0

usage() {
  echo "Usage: $0 <test_name>"
  echo "Tests currently available: "
    TESTS=$(ls -1 jsonnet-integration/*.jsonnet)
    for FILEPATH in $TESTS; do
      TEST_NAME=$(basename -s '.jsonnet' "$FILEPATH")
      echo " - $TEST_NAME"
    done
    exit 1
}

if [[ $# -lt 1 ]]; then
    usage
    exit 1
fi

TEST_NAME=$1
TEST_FILE="jsonnet-integration/$TEST_NAME.jsonnet"
OUTPUT_DIR="jsonnet-integration-tests"

if [ ! -f "$TEST_FILE" ]; then
  echo "Error - unable to find $TEST_FILE"
  usage
  exit 1
fi

set -euo pipefail

if [ ! -d "$OUTPUT_DIR" ]; then
  mkdir "$OUTPUT_DIR"
  (
    cd "$OUTPUT_DIR"

    # Initialise the Tanka.
    tk init --k8s=1.29

    # Install rollout-operator from this branch.
    jb install ../../operations/rollout-operator
  )
fi

export PAGER=cat

echo "Initialise $TEST_NAME"
rm -rf "$OUTPUT_DIR/environments/$TEST_NAME"
mkdir -p "$OUTPUT_DIR/environments/$TEST_NAME/yaml"
cp "$TEST_FILE" "$OUTPUT_DIR/environments/$TEST_NAME/main.jsonnet"
cp "$OUTPUT_DIR/environments/default/spec.json" "$OUTPUT_DIR/environments/$TEST_NAME/spec.json"

(
  cd "$OUTPUT_DIR"
  echo "Compiling $TEST_NAME"
  tk export "environments/$TEST_NAME/yaml" "environments/$TEST_NAME"
)
