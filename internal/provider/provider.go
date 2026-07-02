// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"encoding/json"
	"fmt"
	"time"

	chkv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse-keeper.altinity.com/v1"
	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/scaledb-io/provider-altinity-clickhouse/internal/common"
)

// Compile-time check.
var _ controller.ProviderInterface = (*Provider)(nil)

// Provider implements controller.ProviderInterface for ClickHouse via the Altinity operator.
type Provider struct {
	controller.BaseProvider
}

// New creates a new Provider instance.
func New() *Provider {
	return &Provider{
		BaseProvider: controller.BaseProvider{
			ProviderName: common.ProviderName,
			SchemeFuncs: []func(*runtime.Scheme) error{
				chiv1.AddToScheme,
				chkv1.AddToScheme,
			},
			// NOTE: We intentionally do NOT watch CHI/CHK here.
			// Watching them causes a tight feedback loop: operator updates
			// (finalizers, status) re-trigger Apply, which updates the object,
			// which triggers the operator again.
			// Instead, Status() polls via c.Get() on each Instance reconcile,
			// and Sync() returns WaitError while provisioning is in progress.
			WatchConfigs: []controller.WatchConfig{},
		},
	}
}

// Validate checks the Instance spec before reconciliation.
func (p *Provider) Validate(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating ClickHouse instance", "name", c.Name())

	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok {
		return fmt.Errorf("engine component is required")
	}

	if engine.Resources != nil && engine.Resources.Limits != nil {
		lim := engine.Resources.Limits
		if cpu := lim.Cpu(); cpu != nil && !cpu.IsZero() {
			if cpu.Cmp(resource.MustParse("1")) < 0 {
				return fmt.Errorf("engine CPU limit must be at least 1 core")
			}
		}
		if mem := lim.Memory(); mem != nil && !mem.IsZero() {
			if mem.Cmp(resource.MustParse("1Gi")) < 0 {
				return fmt.Errorf("engine memory limit must be at least 1Gi")
			}
		}
	}

	if c.Instance().GetTopologyType() == common.TopologyReplicated {
		if engine.Replicas != nil && *engine.Replicas < 2 {
			return fmt.Errorf("replicated topology requires at least 2 engine replicas")
		}

		// Reject a silent scale-down. Editing `replicas` down on a running
		// replicated cluster (e.g. 3 -> 2) passes the min-2 check above, but
		// Sync's migration guard only fires when the current replica count is
		// BELOW target — so a decrease would be silently ignored (no scale-down,
		// no error). Decommissioning a replica is a data operation, not an
		// in-place infra edit, so we reject it here. See #7.
		target := replicasCount(c)
		existingCHI := &chiv1.ClickHouseInstallation{}
		switch err := c.Get(existingCHI, c.Name()); {
		case err == nil:
			if replicatedScaleDownRequested(existingCHI, target) {
				return fmt.Errorf(
					"scaling down replicated replicas (%d -> %d) is not supported: "+
						"decommissioning replicas is a data operation — keep replicas at or above the "+
						"current count, or delete and recreate the instance",
					chiReplicaCount(existingCHI), target)
			}
		case !controller.IsNotFound(err):
			// Fail closed on API server/RBAC/network errors rather than letting
			// an unvalidated change through.
			return fmt.Errorf("checking existing ClickHouseInstallation during replicated validation: %w", err)
		}
	}

	// Guard against a replicated -> standalone downgrade. Removing the Keeper
	// and dropping replicas from a running replicated cluster is destructive
	// (loss of HA, and data loss on ReplicatedMergeTree tables), so we reject
	// it. A downgrade must be done by recreating the Instance. We detect the
	// prior replicated state by the presence of the instance's Keeper CR.
	if c.Instance().GetTopologyType() == common.TopologyStandalone {
		chk := &chkv1.ClickHouseKeeperInstallation{}
		err := c.Get(chk, keeperCRName(c.Name()))
		switch {
		case err == nil:
			// The Keeper exists, so this is a replicated instance being
			// downgraded to standalone.
			return fmt.Errorf("cannot downgrade a replicated instance to standalone: " +
				"delete and recreate the instance instead")
		case !controller.IsNotFound(err):
			// Fail closed on API server/RBAC/network errors. Treating an
			// unknown error as "no Keeper" would let a destructive downgrade
			// through while the cluster is unhealthy.
			return fmt.Errorf("checking for existing Keeper during standalone validation: %w", err)
		}
	}

	return nil
}

// Sync creates and polls the required resources for the selected topology.
//
// Create-only semantics: once created, the Altinity operator owns the CHI/CHK
// and we must not overwrite its changes on every reconcile. WaitError is
// returned while provisioning is in progress so the runtime requeues after 15s.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	topology := c.Instance().GetTopologyType()
	l.Info("Syncing ClickHouse instance", "name", c.Name(), "topology", topology)

	switch topology {
	case common.TopologyReplicated:
		return p.syncReplicated(c)
	default:
		// standalone (and any unknown topology falls through to standalone)
		return p.syncStandalone(c)
	}
}

// syncStandalone creates or waits on the CHI for a single-node deployment.
func (p *Provider) syncStandalone(c *controller.Context) error {
	l := log.FromContext(c.Context())

	existing := &chiv1.ClickHouseInstallation{}
	if err := c.Get(existing, c.Name()); err != nil {
		chi, buildErr := buildCHI(c, 1)
		if buildErr != nil {
			return fmt.Errorf("build ClickHouseInstallation: %w", buildErr)
		}
		if applyErr := c.Apply(chi); applyErr != nil {
			return fmt.Errorf("create ClickHouseInstallation: %w", applyErr)
		}
		l.Info("ClickHouseInstallation created", "name", c.Name())
		return controller.WaitForDuration("waiting for Altinity operator to provision ClickHouseInstallation", 15*time.Second)
	}

	return waitForCHI(c, existing)
}

// syncReplicated creates or waits on a CHK (Keeper) + CHI pair.
func (p *Provider) syncReplicated(c *controller.Context) error {
	l := log.FromContext(c.Context())
	keeperName := keeperCRName(c.Name())

	// 1. Ensure Keeper exists.
	existingCHK := &chkv1.ClickHouseKeeperInstallation{}
	if err := c.Get(existingCHK, keeperName); err != nil {
		chk := buildCHK(c)
		if applyErr := c.Apply(chk); applyErr != nil {
			return fmt.Errorf("create ClickHouseKeeperInstallation: %w", applyErr)
		}
		l.Info("ClickHouseKeeperInstallation created", "name", keeperName)
		return controller.WaitForDuration("waiting for Keeper to initialize", 15*time.Second)
	}

	// 2. Wait for Keeper to be ready before creating ClickHouse.
	if keeperErr := waitForCHK(c, existingCHK); keeperErr != nil {
		return keeperErr
	}

	// 3. Ensure ClickHouse exists.
	replicas := replicasCount(c)
	existingCHI := &chiv1.ClickHouseInstallation{}
	if err := c.Get(existingCHI, c.Name()); err != nil {
		chi, buildErr := buildCHI(c, replicas)
		if buildErr != nil {
			return fmt.Errorf("build ClickHouseInstallation: %w", buildErr)
		}
		if applyErr := c.Apply(chi); applyErr != nil {
			return fmt.Errorf("create ClickHouseInstallation: %w", applyErr)
		}
		l.Info("ClickHouseInstallation created (replicated)", "name", c.Name(), "replicas", replicas)
		return controller.WaitForDuration("waiting for Altinity operator to provision ClickHouseInstallation", 15*time.Second)
	}

	// 3b. Migration path: an existing CHI provisioned as standalone (no Keeper
	// wiring, single replica) is reconfigured in place to the replicated shape.
	// This is the ONLY case where we mutate an existing CHI — steady state stays
	// create-only. The patch is idempotent and guarded, so once the operator has
	// converged (Keeper wired + replicas scaled) we stop touching the CHI.
	//
	// Existing table DATA is intentionally NOT migrated here: non-replicated
	// MergeTree tables stay single-replica until a DBA converts them to
	// ReplicatedMergeTree and backfills. See MIGRATION.md.
	if chiNeedsReplicatedMigration(existingCHI, replicas) {
		if patchErr := patchCHIReplicatedTopology(c, existingCHI, buildZookeeperConfig(c), replicas); patchErr != nil {
			return fmt.Errorf("migrate ClickHouseInstallation to replicated: %w", patchErr)
		}
		l.Info("Migrating ClickHouseInstallation standalone->replicated",
			"name", c.Name(), "replicas", replicas)
		return controller.WaitForDuration("waiting for operator to apply replicated topology", 15*time.Second)
	}

	return waitForCHI(c, existingCHI)
}

// chiNeedsReplicatedMigration reports whether an existing CHI still has the
// standalone shape — no Keeper/ZooKeeper wiring, or fewer replicas than
// desired — and therefore must be reconfigured for the replicated topology.
func chiNeedsReplicatedMigration(chi *chiv1.ClickHouseInstallation, targetReplicas int) bool {
	if chi.Spec.Configuration == nil {
		return true
	}
	if chi.Spec.Configuration.Zookeeper == nil || len(chi.Spec.Configuration.Zookeeper.Nodes) == 0 {
		return true
	}
	return chiReplicaCount(chi) < targetReplicas
}

// replicatedScaleDownRequested reports whether target would shrink an existing
// replicated cluster below its current replica count. Decommissioning replicas
// is a data operation we intentionally do not perform in place, so callers
// reject it rather than silently ignoring the change. See #7.
func replicatedScaleDownRequested(existing *chiv1.ClickHouseInstallation, target int) bool {
	return chiReplicaCount(existing) > target
}

// chiReplicaCount returns the replica count of the CHI's first cluster,
// defaulting to 1 when the layout is not set.
func chiReplicaCount(chi *chiv1.ClickHouseInstallation) int {
	if chi.Spec.Configuration == nil || len(chi.Spec.Configuration.Clusters) == 0 {
		return 1
	}
	layout := chi.Spec.Configuration.Clusters[0].Layout
	if layout == nil || layout.ReplicasCount == 0 {
		return 1
	}
	return layout.ReplicasCount
}

// jsonPatchOp is a single RFC 6902 JSON Patch operation.
type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// buildReplicatedMigrationPatch returns the surgical JSON Patch that wires
// ClickHouse Keeper (ZooKeeper node list) and scales the cluster to the target
// replica count — and touches nothing else. An "add" on an existing object
// member replaces its value (and creates it when absent), so it is correct
// whether or not ZooKeeper was previously wired.
//
// This is deliberately NOT a full-object write: the Altinity operator owns and
// continuously normalizes the CHI, so we only ever assert the two fields we
// own. Kept free of *controller.Context so it is unit-testable. See #8.
func buildReplicatedMigrationPatch(chi *chiv1.ClickHouseInstallation, zk *chiv1.ZookeeperConfig, replicas int) ([]jsonPatchOp, error) {
	if chi.Spec.Configuration == nil ||
		len(chi.Spec.Configuration.Clusters) == 0 ||
		chi.Spec.Configuration.Clusters[0] == nil ||
		chi.Spec.Configuration.Clusters[0].Layout == nil {
		return nil, fmt.Errorf("CHI %q lacks the expected standalone shape (configuration/clusters[0]/layout); refusing to patch", chi.Name)
	}
	return []jsonPatchOp{
		{Op: "add", Path: "/spec/configuration/zookeeper", Value: zk},
		{Op: "add", Path: "/spec/configuration/clusters/0/layout/replicasCount", Value: replicas},
	}, nil
}

// patchCHIReplicatedTopology applies buildReplicatedMigrationPatch as an RFC
// 6902 JSON Patch. Unlike a full client.Update off a possibly-stale Get, a
// targeted JSON Patch (a) carries no resourceVersion, so it cannot conflict
// with concurrent operator writes, and (b) mutates only the two field paths we
// own, so operator-normalized spec fields are never clobbered. See #8.
func patchCHIReplicatedTopology(c *controller.Context, chi *chiv1.ClickHouseInstallation, zk *chiv1.ZookeeperConfig, replicas int) error {
	ops, err := buildReplicatedMigrationPatch(chi, zk, replicas)
	if err != nil {
		return err
	}
	data, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("marshal migration patch: %w", err)
	}
	return c.Client().Patch(c.Context(), chi, client.RawPatch(types.JSONPatchType, data))
}

// waitForCHI checks CHI status and returns a WaitError if not yet Completed.
func waitForCHI(c *controller.Context, chi *chiv1.ClickHouseInstallation) error {
	l := log.FromContext(c.Context())

	if chi.Status == nil {
		return controller.WaitForDuration("waiting for Altinity operator to initialize CHI", 15*time.Second)
	}
	switch chi.Status.GetStatus() {
	case chiv1.StatusCompleted:
		l.Info("ClickHouseInstallation is Completed", "name", chi.Name)
		return nil
	case chiv1.StatusAborted:
		return fmt.Errorf("ClickHouseInstallation aborted: %s", chi.Status.GetError())
	default:
		l.Info("ClickHouseInstallation still provisioning", "name", chi.Name, "status", chi.Status.GetStatus())
		return controller.WaitForDuration("waiting for Altinity operator to complete CHI provisioning", 15*time.Second)
	}
}

// waitForCHK checks CHK status and returns a WaitError if not yet Completed.
func waitForCHK(c *controller.Context, chk *chkv1.ClickHouseKeeperInstallation) error {
	l := log.FromContext(c.Context())

	if chk.Status == nil {
		return controller.WaitForDuration("waiting for Keeper to initialize", 15*time.Second)
	}
	switch chk.Status.GetStatus() {
	case chkv1.StatusCompleted:
		l.Info("ClickHouseKeeperInstallation is Completed", "name", chk.Name)
		return nil
	case chkv1.StatusAborted:
		return fmt.Errorf("ClickHouseKeeperInstallation aborted: %s", chk.Status.GetError())
	default:
		l.Info("Keeper still provisioning", "name", chk.Name, "status", chk.Status.GetStatus())
		return controller.WaitForDuration("waiting for Keeper to complete provisioning", 15*time.Second)
	}
}

// Status reports the current status of the ClickHouse instance.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	topology := c.Instance().GetTopologyType()

	if topology == common.TopologyReplicated {
		// For replicated, check Keeper first, then CHI.
		chk := &chkv1.ClickHouseKeeperInstallation{}
		if err := c.Get(chk, keeperCRName(c.Name())); err != nil {
			return controller.Provisioning("Waiting for ClickHouseKeeperInstallation"), nil
		}
		if chk.Status == nil || chk.Status.GetStatus() != chkv1.StatusCompleted {
			return controller.Provisioning("Waiting for Keeper to become ready"), nil
		}
	}

	chi := &chiv1.ClickHouseInstallation{}
	if err := c.Get(chi, c.Name()); err != nil {
		return controller.Provisioning("Waiting for ClickHouseInstallation"), nil
	}
	if chi.Status == nil {
		return controller.Provisioning("Waiting for operator to initialize"), nil
	}

	switch chi.Status.GetStatus() {
	case chiv1.StatusCompleted:
		return controller.ReadyWithConnectionDetails(buildConnectionDetails(c, chi)), nil
	case chiv1.StatusAborted:
		errMsg := chi.Status.GetError()
		if errMsg == "" {
			errMsg = "ClickHouseInstallation aborted"
		}
		return controller.Failed(errMsg), nil
	default:
		return controller.Provisioning(fmt.Sprintf("Cluster is being created (%s)", chi.Status.GetStatus())), nil
	}
}

// Cleanup removes the CHI (and CHK for replicated) when the Instance is deleted.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up ClickHouse instance", "name", c.Name())

	chi := &chiv1.ClickHouseInstallation{ObjectMeta: c.ObjectMeta(c.Name())}
	if err := c.Delete(chi); err != nil {
		return fmt.Errorf("delete ClickHouseInstallation: %w", err)
	}

	// Always attempt to delete the Keeper, regardless of the Instance's current
	// topology label. A replicated instance that is edited back toward standalone,
	// or an Instance deleted during a provider outage, can leave the topology
	// reading "standalone" while a migrated Keeper still exists — gating the
	// deletion on the label here would orphan it. c.Delete ignores NotFound, so
	// this is a harmless no-op for an instance that never had a Keeper.
	chk := &chkv1.ClickHouseKeeperInstallation{ObjectMeta: c.ObjectMeta(keeperCRName(c.Name()))}
	if err := c.Delete(chk); err != nil {
		return fmt.Errorf("delete ClickHouseKeeperInstallation: %w", err)
	}

	l.Info("ClickHouse instance cleaned up", "name", c.Name())
	return nil
}

// =============================================================================
// Builders
// =============================================================================

// buildCHI constructs a ClickHouseInstallation for the given replica count.
func buildCHI(c *controller.Context, replicasCount int) (*chiv1.ClickHouseInstallation, error) {
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	image, err := resolveImage(c, engine)
	if err != nil {
		return nil, err
	}
	cpu, memory := resolveResources(engine)
	storageSize, storageClass := resolveStorage(engine)

	container := corev1.Container{
		Name:  "clickhouse",
		Image: image,
		Resources: corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory},
			Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory},
		},
	}

	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: storageSize}},
	}
	if storageClass != nil {
		pvcSpec.StorageClassName = storageClass
	}

	cluster := &chiv1.Cluster{
		Name: common.CHIClusterName,
		Layout: &chiv1.ChiClusterLayout{
			ShardsCount:   1,
			ReplicasCount: replicasCount,
		},
		Templates: &chiv1.TemplatesList{
			PodTemplate:             common.PodTemplateName,
			DataVolumeClaimTemplate: common.DataVolumeClaimTemplateName,
		},
	}

	spec := chiv1.ChiSpec{
		Configuration: &chiv1.Configuration{
			Clusters: []*chiv1.Cluster{cluster},
		},
		Templates: &chiv1.Templates{
			PodTemplates:         []chiv1.PodTemplate{{Name: common.PodTemplateName, Spec: corev1.PodSpec{Containers: []corev1.Container{container}}}},
			VolumeClaimTemplates: []chiv1.VolumeClaimTemplate{{Name: common.DataVolumeClaimTemplateName, Spec: pvcSpec}},
		},
	}

	// Wire Keeper for replicated topology using explicit node listing.
	// The Altinity operator creates per-replica headless services following:
	//   chk-<keeper-name>-<cluster>-0-<replica-index>.<namespace>.svc
	// We enumerate them for the ZooKeeper config so older operator versions
	// (which may not support the keeper: reference field) work correctly.
	if replicasCount > 1 {
		spec.Configuration.Zookeeper = buildZookeeperConfig(c)
	}

	return &chiv1.ClickHouseInstallation{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec:       spec,
	}, nil
}

// buildCHK constructs a ClickHouseKeeperInstallation with 3 replicas (Raft quorum).
func buildCHK(c *controller.Context) *chkv1.ClickHouseKeeperInstallation {
	// Keeper nodes are small — fixed resources, not user-configurable.
	keeperCPU := resource.MustParse("500m")
	keeperMem := resource.MustParse("1Gi")
	keeperStorage := resource.MustParse("10Gi")

	container := corev1.Container{
		Name:  "clickhouse-keeper",
		Image: "clickhouse/clickhouse-keeper:25.3.5",
		Resources: corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceCPU: keeperCPU, corev1.ResourceMemory: keeperMem},
			Requests: corev1.ResourceList{corev1.ResourceCPU: keeperCPU, corev1.ResourceMemory: keeperMem},
		},
	}

	return &chkv1.ClickHouseKeeperInstallation{
		ObjectMeta: c.ObjectMeta(keeperCRName(c.Name())),
		Spec: chkv1.ChkSpec{
			Configuration: &chkv1.Configuration{
				Clusters: []*chkv1.Cluster{
					{
						Name: common.CHKClusterName,
						Layout: &chkv1.ChkClusterLayout{
							ReplicasCount: common.KeeperReplicas,
						},
						Templates: &chiv1.TemplatesList{
							PodTemplate:             common.KeeperPodTemplateName,
							DataVolumeClaimTemplate: common.KeeperDataVolumeClaimTemplateName,
						},
					},
				},
			},
			Templates: &chiv1.Templates{
				PodTemplates: []chiv1.PodTemplate{
					{
						Name: common.KeeperPodTemplateName,
						Spec: corev1.PodSpec{Containers: []corev1.Container{container}},
					},
				},
				VolumeClaimTemplates: []chiv1.VolumeClaimTemplate{
					{
						Name: common.KeeperDataVolumeClaimTemplateName,
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceStorage: keeperStorage},
							},
						},
					},
				},
			},
		},
	}
}

// =============================================================================
// Helpers
// =============================================================================

// keeperCRName returns the CHK resource name for a given instance.
func keeperCRName(instanceName string) string {
	return instanceName + "-keeper"
}

// buildZookeeperConfig returns the ZooKeeper/Keeper client config that points
// the ClickHouse cluster at this instance's ClickHouseKeeperInstallation.
// Shared by the create path (buildCHI) and the standalone->replicated
// migration path so both stay in sync.
func buildZookeeperConfig(c *controller.Context) *chiv1.ZookeeperConfig {
	return &chiv1.ZookeeperConfig{
		Nodes:              keeperZookeeperNodes(keeperCRName(c.Name()), c.Namespace(), common.KeeperReplicas),
		SessionTimeoutMs:   30000,
		OperationTimeoutMs: 10000,
	}
}

// keeperZookeeperNodes builds the explicit ZooKeeper node list for Keeper.
// Altinity creates per-replica headless services:
//
//	chk-<keeper-name>-<cluster>-0-<replica>.<namespace>.svc
//
// Port 2181 is the standard ZooKeeper/Keeper client port.
func keeperZookeeperNodes(keeperName, namespace string, replicas int) []chiv1.ZookeeperNode {
	nodes := make([]chiv1.ZookeeperNode, replicas)
	for i := 0; i < replicas; i++ {
		// Service name pattern: chk-<keeper-name>-<cluster>-<shard>-<replica>
		svc := fmt.Sprintf("chk-%s-%s-0-%d.%s.svc", keeperName, common.CHKClusterName, i, namespace)
		nodes[i] = chiv1.NewZookeeperNode(svc, 2181)
	}
	return nodes
}

// replicasCount returns the configured replica count or the default.
func replicasCount(c *controller.Context) int {
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	if engine.Replicas != nil && *engine.Replicas >= 2 {
		return int(*engine.Replicas)
	}
	return common.DefaultReplicasCount
}

// resolveImage returns the container image for the engine component.
func resolveImage(c *controller.Context, engine corev1alpha1.ComponentSpec) (string, error) {
	if engine.Image != "" {
		return engine.Image, nil
	}
	spec, err := c.ProviderSpec()
	if err != nil {
		return "", fmt.Errorf("get provider spec: %w", err)
	}
	if engine.Version != "" {
		if img := controller.GetImageForVersion(spec, common.ComponentEngine, engine.Version); img != "" {
			return img, nil
		}
	}
	if img := controller.GetDefaultImageForComponent(spec, common.ComponentEngine); img != "" {
		return img, nil
	}
	return "", fmt.Errorf("no image found for engine component")
}

// resolveResources returns CPU and memory quantities with defaults applied.
func resolveResources(engine corev1alpha1.ComponentSpec) (cpu, memory resource.Quantity) {
	cpu = resource.MustParse("1")
	memory = resource.MustParse("4Gi")
	if engine.Resources == nil || engine.Resources.Limits == nil {
		return
	}
	if v := engine.Resources.Limits.Cpu(); v != nil && !v.IsZero() {
		cpu = v.DeepCopy()
	}
	if v := engine.Resources.Limits.Memory(); v != nil && !v.IsZero() {
		memory = v.DeepCopy()
	}
	return
}

// resolveStorage returns the storage size and optional storage class.
func resolveStorage(engine corev1alpha1.ComponentSpec) (size resource.Quantity, storageClass *string) {
	size = resource.MustParse("25Gi")
	if engine.Storage == nil {
		return
	}
	if !engine.Storage.Size.IsZero() {
		size = engine.Storage.Size.DeepCopy()
	}
	storageClass = engine.Storage.StorageClass
	return
}

// buildConnectionDetails extracts connection info from a ready CHI.
func buildConnectionDetails(c *controller.Context, chi *chiv1.ClickHouseInstallation) controller.ConnectionDetails {
	svcName := fmt.Sprintf("clickhouse-%s", c.Name())
	host := chi.Status.GetEndpoint()
	if host == "" {
		host = fmt.Sprintf("%s.%s.svc", svcName, c.Namespace())
	}
	return controller.ConnectionDetails{
		Type:     "clickhouse",
		Provider: common.ProviderName,
		Host:     host,
		Port:     "8123",
		URI:      fmt.Sprintf("http://default:@%s:8123/", host),
	}
}
