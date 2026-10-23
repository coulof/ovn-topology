package ovsdb

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/coulof/ovn-topology/pkg/graph"
	"sigs.k8s.io/yaml"
)

var update = flag.Bool("update", false, "rewrite scenario golden files")

// TestLoadFixtureMatchesHandParser: the libovsdb-decoded loader reads the original
// hand-written fixture into the same objects as graph.LoadTestSnapshot.
func TestLoadFixtureMatchesHandParser(t *testing.T) {
	got, err := LoadFixture("../../testdata")
	if err != nil {
		t.Fatal(err)
	}
	want := graph.LoadTestSnapshot(t)

	for name, pair := range map[string][2]int{
		"Logical_Router":      {len(got.LogicalRouters), len(want.LogicalRouters)},
		"Logical_Router_Port": {len(got.LogicalRouterPorts), len(want.LogicalRouterPorts)},
		"Logical_Switch":      {len(got.LogicalSwitches), len(want.LogicalSwitches)},
		"Logical_Switch_Port": {len(got.LogicalSwitchPorts), len(want.LogicalSwitchPorts)},
		"NAT":                 {len(got.NATs), len(want.NATs)},
		"Gateway_Chassis":     {len(got.GatewayChassis), len(want.GatewayChassis)},
		"Port_Binding":        {len(got.PortBindings), len(want.PortBindings)},
		"Encap":               {len(got.Encaps), len(want.Encaps)},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: %d rows, hand parser has %d", name, pair[0], pair[1])
		}
	}
	for uuid, ls := range want.LogicalSwitches {
		g := got.LogicalSwitches[uuid]
		if g == nil || g.Name != ls.Name || len(g.Ports) != len(ls.Ports) {
			t.Errorf("switch %s differs: %+v vs %+v", uuid, g, ls)
		}
	}
	for uuid, lsp := range want.LogicalSwitchPorts {
		g := got.LogicalSwitchPorts[uuid]
		if g == nil || g.Name != lsp.Name || g.Type != lsp.Type || len(g.Addresses) != len(lsp.Addresses) {
			t.Errorf("port %s differs: %+v vs %+v", uuid, g, lsp)
		}
	}
	l3 := got.BuildL3View("", 0)
	if len(l3.Nodes) != len(want.BuildL3View("", 0).Nodes) {
		t.Errorf("L3 view: %d nodes, hand parser gives %d", len(l3.Nodes), len(want.BuildL3View("", 0).Nodes))
	}
}

// TestScenarioGoldens renders every captured scenario (testdata/scenarios/<name>/,
// see hack/capture-fixtures.sh) in each view and compares with its golden files.
// Run with -update after capturing a new scenario, and review the diff.
func TestScenarioGoldens(t *testing.T) {
	dirs, _ := filepath.Glob("../../testdata/scenarios/*/" + FixtureNB)
	if len(dirs) == 0 {
		t.Skip("no captured scenarios in testdata/scenarios")
	}
	for _, nbFile := range dirs {
		dir := filepath.Dir(nbFile)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			snap, err := LoadFixture(dir)
			if err != nil {
				t.Fatal(err)
			}
			views := map[string]any{
				"l3":      snap.BuildL3View("", 0),
				"chassis": snap.BuildChassisView("", 0),
			}
			// L2 of every switch, keyed by switch name.
			for id, sw := range snap.LogicalSwitches {
				l2, err := snap.BuildL2View(graph.PrefixLS+id, 1, 1000)
				if err != nil {
					t.Fatal(err)
				}
				views["l2-"+sw.Name] = l2
			}
			for name, v := range views {
				compareGolden(t, filepath.Join(dir, "golden", name+".yaml"), v)
			}
		})
	}
}

func compareGolden(t *testing.T, path string, v any) {
	t.Helper()
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := yaml.JSONToYAML(j)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./pkg/ovsdb -run TestScenarioGoldens -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file (rerun with -update and review the diff)", path)
	}
}
