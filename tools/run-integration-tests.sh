#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Run integration tests, optionally sharded across CI runners.
# Usage: SHARD_INDEX=0 SHARD_COUNT=2 ./tools/run-integration-tests.sh
# When SHARD_COUNT is unset or <= 1, runs the full suite.

set -euo pipefail

SHARD_INDEX="${SHARD_INDEX:-0}"
SHARD_COUNT="${SHARD_COUNT:-1}"

if [ "${SHARD_COUNT}" -le 1 ]; then
	go test -v -tags requires_docker -count 1 -timeout 1h ./integration/...
	exit 0
fi

if [ "${SHARD_INDEX}" -lt 0 ] || [ "${SHARD_INDEX}" -ge "${SHARD_COUNT}" ]; then
	echo "SHARD_INDEX (${SHARD_INDEX}) must be in [0, ${SHARD_COUNT})" >&2
	exit 1
fi

listed=$(go test -tags requires_docker -list "Test.*" ./integration/...)
tests=$(printf '%s\n' "${listed}" | awk -v n="${SHARD_COUNT}" -v i="${SHARD_INDEX}" \
	'/^Test/ { if ((c++ % n) == i) print }')

if [ -z "${tests}" ]; then
	echo "No integration tests for shard ${SHARD_INDEX}/${SHARD_COUNT}"
	exit 0
fi

pattern=$(printf '%s\n' "${tests}" | paste -sd '|' -)
echo "Running shard ${SHARD_INDEX}/${SHARD_COUNT}:"
printf '%s\n' "${tests}"
go test -v -tags requires_docker -count 1 -timeout 1h -run "^(${pattern})$" ./integration/...
