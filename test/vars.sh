#!/usr/bin/env bash
# Environment for the kuttl integration suite (`make test-integration`).
#
# These are sourced by the Makefile before invoking kuttl and are consumed by
# the scripted steps (e.g. the ReplicatedMergeTree replication check). Override
# any of them in your shell before running `make test-integration`.
#
# NOTE: no secrets belong in this file — it is committed to the repo.

# Namespace the test Instance and its ClickHouse/Keeper resources live in.
# Must match the namespace in the test case manifests (metadata.namespace).
export TEST_NAMESPACE="${TEST_NAMESPACE:-default}"

# Name of the OpenEverest Instance under test. Must match the manifests.
export TEST_INSTANCE="${TEST_INSTANCE:-itest-ch}"

# Per-step timeout (seconds) for kuttl assertions. Provisioning ClickHouse +
# Keeper on a cold k3d cluster (image pulls) can be slow, so keep this generous.
export KUTTL_TIMEOUT="${KUTTL_TIMEOUT:-600}"
