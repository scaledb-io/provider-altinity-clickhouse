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
	"fmt"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/scaledb-io/provider-clickhouse/internal/common"
)

// Compile-time check that Provider implements the required interface.
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
			},
			WatchConfigs: []controller.WatchConfig{
				controller.WatchOwned(&chiv1.ClickHouseInstallation{}),
			},
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

	return nil
}

// Sync creates or updates the ClickHouseInstallation resource.
func (p *Provider) Sync(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing ClickHouse instance", "name", c.Name())

	chi, err := buildCHI(c)
	if err != nil {
		return fmt.Errorf("build ClickHouseInstallation: %w", err)
	}

	if err := c.Apply(chi); err != nil {
		return fmt.Errorf("apply ClickHouseInstallation: %w", err)
	}

	l.Info("ClickHouse instance synced", "name", c.Name())
	return nil
}

// Status reports the current status of the ClickHouse instance.
func (p *Provider) Status(c *controller.Context) (controller.Status, error) {
	chi := &chiv1.ClickHouseInstallation{}
	if err := c.Get(chi, c.Name()); err != nil {
		return controller.Provisioning("Waiting for ClickHouseInstallation"), nil
	}

	if chi.Status == nil {
		return controller.Provisioning("Waiting for operator to initialize"), nil
	}

	switch chi.Status.GetStatus() {
	case chiv1.StatusCompleted:
		details := buildConnectionDetails(c, chi)
		return controller.ReadyWithConnectionDetails(details), nil
	case chiv1.StatusAborted:
		errMsg := chi.Status.GetError()
		if errMsg == "" {
			errMsg = "ClickHouseInstallation aborted"
		}
		return controller.Failed(errMsg), nil
	default:
		// InProgress, Terminating, or empty — still provisioning
		return controller.Provisioning(fmt.Sprintf("Cluster is being created (%s)", chi.Status.GetStatus())), nil
	}
}

// Cleanup removes the ClickHouseInstallation when the Instance is deleted.
func (p *Provider) Cleanup(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up ClickHouse instance", "name", c.Name())

	chi := &chiv1.ClickHouseInstallation{
		ObjectMeta: c.ObjectMeta(c.Name()),
	}
	if err := c.Delete(chi); err != nil {
		return fmt.Errorf("delete ClickHouseInstallation: %w", err)
	}

	l.Info("ClickHouse instance cleaned up", "name", c.Name())
	return nil
}

// buildCHI constructs a ClickHouseInstallation from the Instance spec.
func buildCHI(c *controller.Context) (*chiv1.ClickHouseInstallation, error) {
	engine := c.Instance().Spec.Components[common.ComponentEngine]

	// Resolve the container image: explicit override > version bundle > default.
	image, err := resolveImage(c, engine)
	if err != nil {
		return nil, err
	}

	// Resolve resource limits with defaults.
	cpu, memory := resolveResources(engine)

	// Resolve data storage size and optional storage class.
	storageSize, storageClass := resolveStorage(engine)

	// Build the pod template.
	container := corev1.Container{
		Name:  "clickhouse",
		Image: image,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    cpu,
				corev1.ResourceMemory: memory,
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    cpu,
				corev1.ResourceMemory: memory,
			},
		},
	}

	podTemplate := chiv1.PodTemplate{
		Name: common.PodTemplateName,
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{container},
		},
	}

	// Build the volume claim template.
	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: storageSize,
			},
		},
	}
	if storageClass != nil {
		pvcSpec.StorageClassName = storageClass
	}

	vct := chiv1.VolumeClaimTemplate{
		Name: common.DataVolumeClaimTemplateName,
		Spec: pvcSpec,
	}

	// Build the CHI cluster layout (standalone: 1 shard, 1 replica).
	cluster := &chiv1.Cluster{
		Name: common.CHIClusterName,
		Layout: &chiv1.ChiClusterLayout{
			ShardsCount:   1,
			ReplicasCount: 1,
		},
		Templates: &chiv1.TemplatesList{
			PodTemplate:             common.PodTemplateName,
			DataVolumeClaimTemplate: common.DataVolumeClaimTemplateName,
		},
	}

	chi := &chiv1.ClickHouseInstallation{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec: chiv1.ChiSpec{
			Configuration: &chiv1.Configuration{
				Clusters: []*chiv1.Cluster{cluster},
			},
			Templates: &chiv1.Templates{
				PodTemplates:         []chiv1.PodTemplate{podTemplate},
				VolumeClaimTemplates: []chiv1.VolumeClaimTemplate{vct},
			},
		},
	}

	return chi, nil
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
		img := controller.GetImageForVersion(spec, common.ComponentEngine, engine.Version)
		if img != "" {
			return img, nil
		}
	}

	img := controller.GetDefaultImageForComponent(spec, common.ComponentEngine)
	if img == "" {
		return "", fmt.Errorf("no image found for engine component")
	}
	return img, nil
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

// resolveStorage returns the storage size and optional storage class from the engine spec.
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
// Altinity creates a load-balancer service named: chi-<name>-<cluster>
func buildConnectionDetails(c *controller.Context, chi *chiv1.ClickHouseInstallation) controller.ConnectionDetails {
	// Altinity LB service naming: chi-<name>-<clusterName>
	svcName := fmt.Sprintf("chi-%s-%s", c.Name(), common.CHIClusterName)

	// Prefer the status endpoint if already resolved by the operator.
	host := chi.Status.GetEndpoint()
	if host == "" {
		host = fmt.Sprintf("%s.%s.svc", svcName, c.Namespace())
	}

	const httpPort = "8123"

	return controller.ConnectionDetails{
		Type:     "clickhouse",
		Provider: common.ProviderName,
		Host:     host,
		Port:     httpPort,
		URI:      fmt.Sprintf("http://default:@%s:%s/", host, httpPort),
	}
}
