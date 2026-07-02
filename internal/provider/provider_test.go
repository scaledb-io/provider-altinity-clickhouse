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
	"testing"

	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
)

// standaloneCHI returns a CHI shaped the way syncStandalone provisions it:
// single replica, one cluster, no ZooKeeper/Keeper wiring.
func standaloneCHI() *chiv1.ClickHouseInstallation {
	return &chiv1.ClickHouseInstallation{
		Spec: chiv1.ChiSpec{
			Configuration: &chiv1.Configuration{
				Clusters: []*chiv1.Cluster{{
					Name:   "clickhouse",
					Layout: &chiv1.ChiClusterLayout{ShardsCount: 1, ReplicasCount: 1},
				}},
			},
		},
	}
}

func testZK() *chiv1.ZookeeperConfig {
	return &chiv1.ZookeeperConfig{
		Nodes:              []chiv1.ZookeeperNode{chiv1.NewZookeeperNode("chk-x-keeper-0-0.ns.svc", 2181)},
		SessionTimeoutMs:   30000,
		OperationTimeoutMs: 10000,
	}
}

func TestChiReplicaCount(t *testing.T) {
	tests := []struct {
		name string
		chi  *chiv1.ClickHouseInstallation
		want int
	}{
		{"nil configuration", &chiv1.ClickHouseInstallation{}, 1},
		{"no clusters", &chiv1.ClickHouseInstallation{Spec: chiv1.ChiSpec{Configuration: &chiv1.Configuration{}}}, 1},
		{"nil layout", &chiv1.ClickHouseInstallation{Spec: chiv1.ChiSpec{Configuration: &chiv1.Configuration{Clusters: []*chiv1.Cluster{{Name: "c"}}}}}, 1},
		{"zero replicas", standaloneWithReplicas(0), 1},
		{"single replica", standaloneCHI(), 1},
		{"three replicas", standaloneWithReplicas(3), 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chiReplicaCount(tt.chi); got != tt.want {
				t.Errorf("chiReplicaCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReplicatedScaleDownRequested(t *testing.T) {
	cases := []struct {
		name            string
		current, target int
		want            bool
	}{
		{"scale down 3->2", 3, 2, true},
		{"scale down 2->1 (defensive)", 2, 1, true},
		{"no change 2->2", 2, 2, false},
		{"scale up 2->3", 2, 3, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replicatedScaleDownRequested(standaloneWithReplicas(tc.current), tc.target)
			if got != tc.want {
				t.Fatalf("replicatedScaleDownRequested(current=%d, target=%d) = %v, want %v",
					tc.current, tc.target, got, tc.want)
			}
		})
	}
}

func standaloneWithReplicas(n int) *chiv1.ClickHouseInstallation {
	chi := standaloneCHI()
	chi.Spec.Configuration.Clusters[0].Layout.ReplicasCount = n
	return chi
}

func TestChiNeedsReplicatedMigration(t *testing.T) {
	// Standalone (no ZK, 1 replica) must migrate.
	if !chiNeedsReplicatedMigration(standaloneCHI(), 2) {
		t.Error("standalone CHI should need migration")
	}

	// Nil configuration must migrate.
	if !chiNeedsReplicatedMigration(&chiv1.ClickHouseInstallation{}, 2) {
		t.Error("nil-config CHI should need migration")
	}

	// ZooKeeper present but with no nodes must migrate.
	chiEmptyZK := standaloneCHI()
	chiEmptyZK.Spec.Configuration.Zookeeper = &chiv1.ZookeeperConfig{}
	if !chiNeedsReplicatedMigration(chiEmptyZK, 2) {
		t.Error("CHI with empty ZooKeeper node list should need migration")
	}

	// ZK wired but replicas below target must migrate.
	chiUnderReplicated := standaloneCHI()
	chiUnderReplicated.Spec.Configuration.Zookeeper = testZK()
	if !chiNeedsReplicatedMigration(chiUnderReplicated, 2) {
		t.Error("CHI with 1 replica but target 2 should need migration")
	}

	// Fully converged (ZK wired + replicas at target) must NOT migrate.
	converged := standaloneWithReplicas(2)
	converged.Spec.Configuration.Zookeeper = testZK()
	if chiNeedsReplicatedMigration(converged, 2) {
		t.Error("converged replicated CHI should not need migration")
	}
}

func TestMigrateCHIToReplicated(t *testing.T) {
	chi := standaloneCHI()
	migrateCHIToReplicated(chi, testZK(), 2)

	if chi.Spec.Configuration.Zookeeper == nil || len(chi.Spec.Configuration.Zookeeper.Nodes) == 0 {
		t.Fatal("expected ZooKeeper to be wired after migration")
	}
	if got := chiReplicaCount(chi); got != 2 {
		t.Errorf("expected 2 replicas after migration, got %d", got)
	}
	if sc := chi.Spec.Configuration.Clusters[0].Layout.ShardsCount; sc != 1 {
		t.Errorf("expected shard count preserved at 1, got %d", sc)
	}

	// Migration must be idempotent: a second pass leaves it converged.
	migrateCHIToReplicated(chi, testZK(), 2)
	if chiNeedsReplicatedMigration(chi, 2) {
		t.Error("CHI should be converged after migration")
	}
}

func TestMigrateCHIToReplicatedNilConfiguration(t *testing.T) {
	chi := &chiv1.ClickHouseInstallation{}
	migrateCHIToReplicated(chi, testZK(), 3)

	if chi.Spec.Configuration == nil {
		t.Fatal("expected configuration to be created")
	}
	if len(chi.Spec.Configuration.Clusters) == 0 {
		t.Fatal("expected a cluster to be created defensively")
	}
	if got := chiReplicaCount(chi); got != 3 {
		t.Errorf("expected 3 replicas, got %d", got)
	}
}
