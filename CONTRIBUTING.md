# Contributing

Thanks for contributing to `provider-altinity-clickhouse` — an
[OpenEverest](https://github.com/openeverest) provider for ClickHouse.

This guide covers local development, the test suites, and how CI runs them.

## Prerequisites

- Go 1.26+
- [Docker](https://docs.docker.com/get-docker/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/), [Helm](https://helm.sh/docs/intro/install/), [k3d](https://k3d.io/)
- [Tilt](https://docs.tilt.dev/install.html) (for the local dev stack)
- [`kubectl-kuttl`](https://kuttl.dev/docs/cli.html) (for integration tests):
  `go install github.com/kudobuilder/kuttl/cmd/kubectl-kuttl@latest`

> **k3d / kind users — bump inotify first.** The Altinity operator uses inotify
> heavily; the default `fs.inotify.max_user_instances=128` causes *silent*
> reconciliation failures. Set it before creating a cluster:
> ```bash
> echo "fs.inotify.max_user_instances = 8192" | sudo tee /etc/sysctl.d/99-k8s.conf
> sudo sysctl --system
> ```

## Code generation

RBAC rules, the Helm chart's generated files, and the provider spec are all
generated from source — never edit `config/rbac/`, `charts/**/generated/`, or
generated specs by hand.

```bash
make generate   # regenerate everything
make verify     # fail if generated files are out of date (this is a CI gate)
```

If you change kubebuilder RBAC markers, `definition/`, or component types, run
`make generate` and commit the result.

## Tests

### Unit tests

Pure-Go, no cluster required. Run on every pull request in CI.

```bash
make test                              # go test ./... -coverprofile cover.out
go test ./... -race -covermode=atomic  # what CI runs
```

### Integration tests (kuttl, end-to-end)

These stand up a **real** cluster (k3d + the released OpenEverest core + this
provider + the bundled Altinity operator) and exercise the provider through
kuttl. See [`test/integration/README.md`](test/integration/README.md) for the
cases.

Because they are heavy (k3d bring-up, image pulls, ClickHouse + Keeper
rollout), they are **not** run on every commit — see [CI](#ci) below.

Run them locally — this is exactly what CI does, so a green local run means a
green CI run:

```bash
# 1. Bump inotify (see Prerequisites) — one time per host.

# 2. Create the k3d cluster.
make k3d-cluster-up

# 3. Bring up the OpenEverest core + this provider.
#    Pre-release core tags are not picked up by Helm's "latest" resolution,
#    so pin the version explicitly.
OPENEVEREST_VERSION=2.0.0-dev.1 tilt ci -f dev/Tiltfile
#    ...or, for an interactive Tilt dashboard while you develop:
#    make dev-up        (see dev/README.md)

# 4. Run the kuttl suite.
make test-integration

# 5. Tear down.
make k3d-cluster-down
```

`make dev-up` and the full Tilt workflow (live-reload, running the core from
source, configuration) are documented in [`dev/README.md`](dev/README.md).

## CI

| Workflow | Runs on | What it does |
|----------|---------|--------------|
| `ci.yaml` | every PR + push to `main` | Lint, Build, Verify-generated, Copyright headers, Helm lint, **Unit tests** (`go test -race` + coverage) |
| `integration.yaml` | **opt-in only** | The kuttl end-to-end suite above (k3d + `tilt ci` + `make test-integration`) |

The integration workflow is deliberately **not** run per-commit and **not** on
merge (cost). Trigger it one of two ways:

- **Add the `run-integration` label** to your pull request. The job runs (and
  re-runs on new pushes) while the label is present.
- **Run it manually** from the Actions tab via *Run workflow*
  (`workflow_dispatch`), optionally overriding the OpenEverest core version.

## Pull requests

- **Sign off your commits (DCO).** Every commit must carry a `Signed-off-by`
  line — the DCO check is a required gate. Use `git commit -s` (and
  `git rebase --signoff` to fix an existing branch).
- **Copyright headers.** Source files (`.go`, `.ts`, `.tsx`) need a license
  header. Add missing ones with:
  ```bash
  python3 hack/add_copyright.py <changed-files>
  ```
- **Keep generated files in sync** (`make verify` must pass).
- **Run `make lint` and `make test` locally** before pushing.
- If your change touches provider reconciliation behaviour, add or update a
  kuttl case under `test/integration/cases/` and run it with the
  `run-integration` label.

## Development patterns

For provider SDK patterns (RBAC, watches, code generation), see
[PROVIDER_DEVELOPMENT.md](https://github.com/openeverest/provider-sdk/blob/main/PROVIDER_DEVELOPMENT.md).
