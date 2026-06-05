// Package common defines shared constants used across the provider.
package common

const (
	// ProviderName is the canonical name of this provider.
	// Must match the Provider CR name registered in OpenEverest.
	ProviderName = "provider-clickhouse"

	// ComponentEngine is the logical name of the ClickHouse engine component.
	ComponentEngine = "engine"

	// ComponentTypeClickHouse is the component type name, matching versions.yaml.
	ComponentTypeClickHouse = "clickhouse"

	// CHIClusterName is the cluster name used inside the ClickHouseInstallation CR.
	// Altinity uses this as part of the pod and service naming scheme.
	CHIClusterName = "clickhouse"

	// PodTemplateName is the name of the pod template defined in the CHI spec.
	PodTemplateName = "default"

	// DataVolumeClaimTemplateName is the name of the volume claim template for data storage.
	DataVolumeClaimTemplateName = "data"
)
