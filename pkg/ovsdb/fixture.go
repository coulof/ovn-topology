package ovsdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/coulof/ovn-topology/pkg/graph"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
	"github.com/coulof/ovn-topology/pkg/ovsdb/sbmodel"
	"github.com/ovn-kubernetes/libovsdb/cache"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	corev1 "k8s.io/api/core/v1"
)

// Fixture file names in a scenario directory, as written by hack/capture-fixtures.sh.
const (
	FixtureNB   = "ovn-nb.json"
	FixtureSB   = "ovn-sb.json"
	FixtureNodes = "nodes.json"
	FixturePods = "pods.json"
	FixtureVMs  = "virtualmachines.json"
	FixtureVMIs = "virtualmachineinstances.json"
	FixtureNADs = "network-attachment-definitions.json"
)

// fixtureDB is the on-disk shape: {"<db name>": {"<table>": {"<uuid>": <row>}}}, rows
// in RFC 7047 JSON notation (["uuid", …], ["set", …], ["map", …]).
type fixtureDB map[string]json.RawMessage

// LoadFixture builds a snapshot from a scenario directory. Rows are decoded by
// libovsdb into its table cache, exactly as monitor updates are, and the snapshot is
// filled from that cache by the code the syncer uses. nodes.json and pods.json
// (optional) feed the Kubernetes identity the daemon would get from its informers.
func LoadFixture(dir string) (*graph.Snapshot, error) {
	nb, err := fixtureCache(filepath.Join(dir, FixtureNB), "OVN_Northbound", nbmodel.FullDatabaseModel, nbmodel.Schema)
	if err != nil {
		return nil, err
	}
	sb, err := fixtureCache(filepath.Join(dir, FixtureSB), "OVN_Southbound", sbmodel.FullDatabaseModel, sbmodel.Schema)
	if err != nil {
		return nil, err
	}

	mapper := graph.NewKubeNodeMapper(nil, nil)
	resolver := graph.NewKubeWorkloadResolver(nil, nil)
	var nodes []*corev1.Node
	if err := readOptionalJSON(filepath.Join(dir, FixtureNodes), &nodes); err != nil {
		return nil, err
	}
	var pods []*corev1.Pod
	if err := readOptionalJSON(filepath.Join(dir, FixturePods), &pods); err != nil {
		return nil, err
	}
	mapper.SetNodes(nodes)
	resolver.SetPods(pods)

	snap := graph.NewEmptySnapshot(1, mapper)
	snap.WorkloadResolver = resolver
	if err := fillSnapshot(context.Background(), nb, sb, snap); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", dir, err)
	}
	snap.Reindex()
	return snap, nil
}

func readOptionalJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// fixtureCache loads one database file into a libovsdb table cache and returns a
// lister over it.
func fixtureCache(path, dbName string, cm func() (model.ClientDBModel, error), schema func() ovsdb.DatabaseSchema) (lister, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file fixtureDB
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var tables map[string]map[string]ovsdb.Row
	if err := json.Unmarshal(file[dbName], &tables); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", path, dbName, err)
	}

	clientModel, err := cm()
	if err != nil {
		return nil, err
	}
	dbModel, errs := model.NewDatabaseModel(schema(), clientModel)
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s model: %v", dbName, errs)
	}
	tc, err := cache.NewTableCache(dbModel, nil, nil)
	if err != nil {
		return nil, err
	}
	updates := ovsdb.TableUpdates2{}
	for table, rows := range tables {
		if _, ok := dbModel.Types()[table]; !ok {
			continue // a table this build does not model
		}
		tu := ovsdb.TableUpdate2{}
		for uuid, row := range rows {
			delete(row, "_uuid")
			delete(row, "_version")
			tu[uuid] = &ovsdb.RowUpdate2{Initial: &row}
		}
		updates[table] = tu
	}
	if err := tc.Populate2(updates); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cacheLister(tc, dbModel), nil
}

// cacheLister lists rows straight from a table cache, as libovsdb's List does.
func cacheLister(tc *cache.TableCache, dbModel model.DatabaseModel) lister {
	return func(_ context.Context, result any) error {
		slice := reflect.ValueOf(result).Elem()
		table := dbModel.FindTable(reflect.PointerTo(slice.Type().Elem()))
		if table == "" {
			return fmt.Errorf("no table for %s", slice.Type())
		}
		for _, m := range tc.Table(table).Rows() {
			slice.Set(reflect.Append(slice, reflect.ValueOf(m).Elem()))
		}
		return nil
	}
}
