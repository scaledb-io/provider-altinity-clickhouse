# Integration tests (kuttl)

End-to-end tests that run against a real Kubernetes cluster with the OpenEverest
core, this provider, and the bundled Altinity ClickHouse operator installed.

## Prerequisites

- A running cluster with OpenEverest core + this provider deployed. The easiest
  way is the local dev stack:

  ```bash
  make dev-up        # k3d cluster + OpenEverest core + provider (Tilt)
  ```

- [`kubectl-kuttl`](https://kuttl.dev/docs/cli.html) on your `PATH`:

  ```bash
  go install github.com/kudobuilder/kuttl/cmd/kubectl-kuttl@latest
  ```

## Run

```bash
make test-integration
```

This sources [`../vars.sh`](../vars.sh) and runs
`kubectl kuttl test --config ./test/integration/kuttl.yaml`.

## Cases

### `topology-migration`

Exercises the `standalone → replicated` in-place migration:

1. Provision a standalone Instance; assert a single-replica CHI and **no** Keeper.
2. Patch the same Instance to `replicated` (replicas: 2).
3. Assert the Keeper is provisioned and `Completed`, and the existing CHI is
   reconfigured to 2 replicas.
4. Create a `ReplicatedMergeTree` table on one replica and confirm the row
   replicates to another — proving the Keeper wiring works end-to-end.
5. Clean up the Instance.
