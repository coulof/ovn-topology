package graph

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/coulof/ovn-topology/pkg/api"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func TestL3View(t *testing.T) {
	snap := LoadTestSnapshot(t)
	resp, err := snap.BuildGraph("l3", "", 1, 1, 50)
	if err != nil {
		t.Fatalf("BuildGraph failed: %v", err)
	}

	if resp.View != api.GraphResponseViewL3 {
		t.Errorf("expected view l3, got %s", resp.View)
	}

	nodeMap := make(map[string]api.GraphNode)
	for _, n := range resp.Nodes {
		nodeMap[n.Id] = n
	}

	// 1. Verify VPC compound nodes
	if _, ok := nodeMap["vpc:default"]; !ok {
		t.Errorf("expected compound node vpc:default")
	}
	if _, ok := nodeMap["vpc:custom-vpc"]; !ok {
		t.Errorf("expected compound node vpc:custom-vpc")
	}

	// 2. Verify LR nodes and counts
	lrDef, ok := nodeMap["lr:lr-default-uuid"]
	if !ok {
		t.Fatalf("expected lr:lr-default-uuid")
	}
	lrDefProps := *lrDef.Properties
	if lrDefProps["routeCount"] != 1 {
		t.Errorf("expected routeCount 1, got %v", lrDefProps["routeCount"])
	}
	if lrDefProps["policyCount"] != 1 {
		t.Errorf("expected policyCount 1, got %v", lrDefProps["policyCount"])
	}
	if lrDefProps["lbCount"] != 1 {
		t.Errorf("expected lbCount 1, got %v", lrDefProps["lbCount"])
	}

	lrCust, ok := nodeMap["lr:lr-custom-uuid"]
	if !ok {
		t.Fatalf("expected lr:lr-custom-uuid")
	}
	lrCustProps := *lrCust.Properties
	if lrCustProps["natCount"] != 1 {
		t.Errorf("expected natCount 1 on custom vpc, got %v", lrCustProps["natCount"])
	}

	// 3. Verify LS nodes and port counts
	lsDef, ok := nodeMap["ls:ls-default-uuid"]
	if !ok {
		t.Fatalf("expected ls:ls-default-uuid")
	}
	lsDefProps := *lsDef.Properties
	if lsDefProps["portCount"] != 4 {
		t.Errorf("expected portCount 4 on default-subnet, got %v", lsDefProps["portCount"])
	}
	if lsDefProps["boundPortCount"] != 2 {
		t.Errorf("expected boundPortCount 2 on default-subnet, got %v", lsDefProps["boundPortCount"])
	}
	if lsDefProps["unboundPortCount"] != 1 {
		t.Errorf("expected unboundPortCount 1 on default-subnet, got %v", lsDefProps["unboundPortCount"])
	}

	// 4. Verify multi-LR join subnet has no single parent (shared non-tree)
	lsJoin, ok := nodeMap["ls:ls-join-uuid"]
	if !ok {
		t.Fatalf("expected ls:ls-join-uuid")
	}
	if lsJoin.Parent != nil {
		t.Errorf("expected multi-LR join subnet to have nil Parent, got %v", *lsJoin.Parent)
	}

	// 5. Verify localnet edge to external node
	if _, ok := nodeMap["ext:provider-ext"]; !ok {
		t.Errorf("expected external node ext:provider-ext")
	}

	edgeMap := make(map[string]api.GraphEdge)
	for _, e := range resp.Edges {
		edgeMap[e.Id] = e
	}

	localnetEdgeID := "edge:ls:ls-ext-uuid-ext:provider-ext-localnet"
	if _, ok := edgeMap[localnetEdgeID]; !ok {
		t.Errorf("expected localnet edge %s", localnetEdgeID)
	}

	// 6. Verify router-peer edges
	foundJoinDefEdge := false
	foundJoinCustEdge := false
	for _, e := range resp.Edges {
		if e.Type == api.RouterPeer {
			if e.Source == "lr:lr-default-uuid" && e.Target == "ls:ls-join-uuid" {
				foundJoinDefEdge = true
			}
			if e.Source == "lr:lr-custom-uuid" && e.Target == "ls:ls-join-uuid" {
				foundJoinCustEdge = true
			}
		}
	}
	if !foundJoinDefEdge || !foundJoinCustEdge {
		t.Errorf("expected join LS to connect to both default LR (%v) and custom LR (%v)", foundJoinDefEdge, foundJoinCustEdge)
	}
}

func TestL2View(t *testing.T) {
	snap := LoadTestSnapshot(t)
	// Focus on default-subnet with pagination (pageSize 2)
	resp, err := snap.BuildGraph("l2", "ls:ls-default-uuid", 1, 1, 2)
	if err != nil {
		t.Fatalf("BuildGraph l2 failed: %v", err)
	}

	if resp.Pagination == nil {
		t.Fatalf("expected pagination info in L2 view")
	}
	if resp.Pagination.TotalItems != 4 {
		t.Errorf("expected totalItems 4, got %d", resp.Pagination.TotalItems)
	}
	if resp.Pagination.TotalPages != 2 {
		t.Errorf("expected totalPages 2, got %d", resp.Pagination.TotalPages)
	}

	// Full page
	fullResp, err := snap.BuildGraph("l2", "ls:ls-default-uuid", 1, 1, 50)
	if err != nil {
		t.Fatalf("BuildGraph full l2 failed: %v", err)
	}

	lspMap := make(map[string]api.GraphNode)
	for _, n := range fullResp.Nodes {
		lspMap[n.Id] = n
	}

	// Verify bound pods
	pod1, ok := lspMap["lsp:lsp-pod1-uuid"]
	if !ok {
		t.Fatalf("expected lsp:lsp-pod1-uuid in L2 view")
	}
	pod1Props := *pod1.Properties
	if pod1Props["boundState"] != "bound" {
		t.Errorf("expected pod1 boundState bound, got %v", pod1Props["boundState"])
	}
	if pod1Props["boundChassis"] != "chassis-node-1" {
		t.Errorf("expected pod1 bound on chassis-node-1, got %v", pod1Props["boundChassis"])
	}

	// Verify unbound pod
	unbound, ok := lspMap["lsp:lsp-unbound-uuid"]
	if !ok {
		t.Fatalf("expected lsp:lsp-unbound-uuid in L2 view")
	}
	unboundProps := *unbound.Properties
	if unboundProps["boundState"] != "unbound" {
		t.Errorf("expected unbound-port boundState unbound, got %v", unboundProps["boundState"])
	}
}

func TestChassisView(t *testing.T) {
	snap := LoadTestSnapshot(t)
	resp, err := snap.BuildGraph("chassis", "", 1, 1, 50)
	if err != nil {
		t.Fatalf("BuildGraph chassis failed: %v", err)
	}

	if len(resp.Nodes) != 3 {
		t.Fatalf("expected 3 chassis nodes, got %d", len(resp.Nodes))
	}

	chassisMap := make(map[string]api.GraphNode)
	for _, n := range resp.Nodes {
		chassisMap[n.Id] = n
	}

	node1, ok := chassisMap["chassis:chassis-node-1"]
	if !ok {
		t.Fatalf("expected chassis:chassis-node-1")
	}
	node1Props := *node1.Properties
	if node1Props["bindingCount"] != 3 {
		t.Errorf("expected node-1 bindingCount 3, got %v", node1Props["bindingCount"])
	}

	node3, ok := chassisMap["chassis:chassis-node-3"]
	if !ok {
		t.Fatalf("expected chassis:chassis-node-3")
	}
	node3Props := *node3.Properties
	if node3Props["bindingCount"] != 0 {
		t.Errorf("expected node-3 (down) bindingCount 0, got %v", node3Props["bindingCount"])
	}
}

func TestNodeDetail(t *testing.T) {
	snap := LoadTestSnapshot(t)

	// 1. LR detail
	lrDetail, err := snap.GetNodeDetail("lr:lr-default-uuid")
	if err != nil {
		t.Fatalf("GetNodeDetail for LR failed: %v", err)
	}
	if lrDetail.Name != "ovn_cluster" {
		t.Errorf("expected LR name ovn_cluster, got %s", lrDetail.Name)
	}
	if len(*lrDetail.Related) == 0 {
		t.Errorf("expected related objects on LR")
	}

	// 2. LS detail
	lsDetail, err := snap.GetNodeDetail("ls:ls-default-uuid")
	if err != nil {
		t.Fatalf("GetNodeDetail for LS failed: %v", err)
	}
	if lsDetail.Name != "default-subnet" {
		t.Errorf("expected LS name default-subnet, got %s", lsDetail.Name)
	}

	// 3. Chassis detail
	chDetail, err := snap.GetNodeDetail("chassis:chassis-node-1")
	if err != nil {
		t.Fatalf("GetNodeDetail for Chassis failed: %v", err)
	}
	if chDetail.Name != "node-1" {
		t.Errorf("expected chassis name node-1, got %s", chDetail.Name)
	}
	// raw is keyed by OVSDB column names, like ovn-sbctl list chassis (not Go field names).
	for _, col := range []string{"_uuid", "name", "hostname", "encaps", "other_config", "external_ids"} {
		if _, ok := chDetail.Raw[col]; !ok {
			t.Errorf("expected OVSDB column %q in chassis raw, got keys %v", col, sortedKeysOf(chDetail.Raw))
		}
	}
	if _, ok := chDetail.Raw["Hostname"]; ok {
		t.Errorf("raw must not use Go field names")
	}
}

func sortedKeysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestGoldenFiles(t *testing.T) {
	snap := LoadTestSnapshot(t)

	goldenDir := filepath.Join("..", "..", "testdata", "golden")
	_ = os.MkdirAll(goldenDir, 0755)

	views := []struct {
		name     string
		view     string
		focus    string
		filename string
	}{
		{"L3 View", "l3", "", "l3_view.json"},
		{"L2 View", "l2", "ls:ls-default-uuid", "l2_view.json"},
		{"Chassis View", "chassis", "", "chassis_view.json"},
	}

	for _, tc := range views {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := snap.BuildGraph(tc.view, tc.focus, 1, 1, 50)
			if err != nil {
				t.Fatalf("BuildGraph %s failed: %v", tc.view, err)
			}
			actualBytes, err := json.MarshalIndent(resp, "", "  ")
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			goldenPath := filepath.Join(goldenDir, tc.filename)
			if *updateGolden {
				if err := os.WriteFile(goldenPath, actualBytes, 0644); err != nil {
					t.Fatalf("failed to update golden file %s: %v", goldenPath, err)
				}
				return
			}

			expectedBytes, err := os.ReadFile(goldenPath)
			if err != nil {
				// If golden file does not exist yet, write it
				if os.IsNotExist(err) {
					if err := os.WriteFile(goldenPath, actualBytes, 0644); err != nil {
						t.Fatalf("failed to write initial golden file: %v", err)
					}
					return
				}
				t.Fatalf("failed to read golden file %s: %v", goldenPath, err)
			}

			if string(actualBytes) != string(expectedBytes) {
				t.Errorf("output does not match golden file %s. Run with -update to regenerate.", tc.filename)
			}
		})
	}
}
