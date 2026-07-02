# Topology Migration: `standalone` → `replicated`

This provider supports migrating an existing Instance from the `standalone`
(single-node) topology to the `replicated` (multi-replica + ClickHouse Keeper)
topology **in place**, without recreating the Instance.

## What the provider does automatically

When you change an existing Instance's topology from `standalone` to
`replicated` (with `replicas >= 2`), the provider:

1. Provisions a `ClickHouseKeeperInstallation` (CHK) — 3-node Raft quorum — and
   waits for it to become `Completed`.
2. Reconfigures the existing `ClickHouseInstallation` (CHI) in place:
   - wires the ClickHouse Keeper (ZooKeeper node list) into the CHI configuration;
   - scales the cluster `ReplicasCount` from 1 up to the target replica count.
3. The Altinity operator rolls the ClickHouse pods, adds the new replica(s), and
   injects the per-replica macros (`{shard}` / `{replica}`) required for
   replicated table engines.

This is the **only** case where the provider mutates an existing CHI — steady
state remains create-only. The reconfigure is idempotent and guarded: once the
operator has converged (Keeper wired + replicas scaled), the provider stops
touching the CHI.

## What the provider does NOT do — table data migration (DBA work)

**Existing table data is not migrated automatically.** This is a deliberate
boundary: converting live tables and backfilling data across replicas is a data
operation that must be planned and verified by a DBA.

After the infrastructure migration completes:

- **New** tables created with a `Replicated*` engine (e.g. `ReplicatedMergeTree`)
  work immediately and replicate across all replicas.
- **Existing** non-replicated `MergeTree` tables remain single-replica. Their
  data lives only on the original node and will **not** appear on the new
  replica(s) until you convert and backfill them.

### Manual table-conversion runbook

For each existing non-replicated table you want to make highly available:

1. Create a new replicated table with the same schema:

   ```sql
   CREATE TABLE db.events_repl AS db.events
   ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/events', '{replica}')
   ORDER BY (...);   -- match the original ORDER BY / PARTITION BY
   ```

2. Copy the data on the node that holds it:

   ```sql
   INSERT INTO db.events_repl SELECT * FROM db.events;
   ```

3. Verify row counts match on every replica (query each replica host directly),
   then swap the tables:

   ```sql
   RENAME TABLE db.events TO db.events_old, db.events_repl TO db.events;
   ```

4. Once replication and row counts are confirmed healthy across all replicas,
   drop the old table:

   ```sql
   DROP TABLE db.events_old;
   ```

> Do this during a maintenance window and validate row counts on each replica
> before dropping anything. For large tables, copy by partition to bound memory
> and lock duration.

## Downgrade is blocked

Going `replicated` → `standalone` is rejected by the provider's `Validate()`
step. Removing the Keeper and dropping replicas from a running replicated
cluster is destructive (loss of HA and potential data loss on
`ReplicatedMergeTree` tables). To downgrade, delete and recreate the Instance as
`standalone`.
