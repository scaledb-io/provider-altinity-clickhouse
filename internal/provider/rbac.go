// Package provider defines the provider implementation.
// RBAC markers for the Altinity ClickHouse and ClickHouse Keeper operator resources.
package provider

// OpenEverest core resources reconciled by the provider-runtime.
// The provider-runtime reconciler watches Instance CRs, manages their
// finalizers/status, and reads the Provider CR. Without these rules the
// controller's ServiceAccount cannot list Instances at cluster scope and the
// pod fails to reconcile ("instances.core.openeverest.io is forbidden").
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch

// Altinity ClickHouseInstallation
// +kubebuilder:rbac:groups=clickhouse.altinity.com,resources=clickhouseinstallations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.altinity.com,resources=clickhouseinstallations/status,verbs=get
// +kubebuilder:rbac:groups=clickhouse.altinity.com,resources=clickhouseinstallations/finalizers,verbs=update

// Altinity ClickHouseKeeperInstallation
// +kubebuilder:rbac:groups=clickhouse-keeper.altinity.com,resources=clickhousekeeperinstallations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse-keeper.altinity.com,resources=clickhousekeeperinstallations/status,verbs=get
// +kubebuilder:rbac:groups=clickhouse-keeper.altinity.com,resources=clickhousekeeperinstallations/finalizers,verbs=update

// Core Kubernetes resources managed by the Altinity operator on our behalf
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
