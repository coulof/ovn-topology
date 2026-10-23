package ovsdb

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coulof/ovn-topology/pkg/graph"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
	"github.com/coulof/ovn-topology/pkg/ovsdb/sbmodel"
	"github.com/ovn-kubernetes/libovsdb/cache"
	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

// fakeClient is a client.Client backed by a real libovsdb TableCache. Rows are fed
// with Populate2, the path monitor updates take in libovsdb, so the syncer sees the
// same cache events as against a server. Only what the syncer calls is implemented.
type fakeClient struct {
	client.Client
	t       *testing.T
	dbModel model.DatabaseModel
	tc      *cache.TableCache
	echoErr atomic.Pointer[error]
	listErr atomic.Pointer[error]
}

func newFakeClient(t *testing.T, cm func() (model.ClientDBModel, error), schema func() ovsdb.DatabaseSchema) *fakeClient {
	t.Helper()
	clientModel, err := cm()
	if err != nil {
		t.Fatal(err)
	}
	dbModel, errs := model.NewDatabaseModel(schema(), clientModel)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	tc, err := cache.NewTableCache(dbModel, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go tc.Run(stop)
	return &fakeClient{t: t, dbModel: dbModel, tc: tc}
}

func (f *fakeClient) Connect(context.Context) error { return nil }
func (f *fakeClient) Close()                        {}
func (f *fakeClient) CurrentEndpoint() string       { return "fake" }
func (f *fakeClient) Cache() *cache.TableCache      { return f.tc }
func (f *fakeClient) MonitorAll(context.Context) (client.MonitorCookie, error) {
	return client.MonitorCookie{}, nil
}

func (f *fakeClient) Echo(context.Context) error {
	if e := f.echoErr.Load(); e != nil {
		return *e
	}
	return nil
}

func (f *fakeClient) setDown(down bool) {
	if !down {
		f.echoErr.Store(nil)
		return
	}
	err := errors.New("not connected")
	f.echoErr.Store(&err)
}

// List mirrors libovsdb's cache-backed List: result is a pointer to a slice of models.
func (f *fakeClient) List(ctx context.Context, result any) error {
	if e := f.listErr.Load(); e != nil {
		return *e
	}
	return cacheLister(f.tc, f.dbModel)(ctx, result)
}

// apply feeds a monitor update; kind is "initial", "insert" or "modify".
func (f *fakeClient) apply(table, kind, uuid string, row ovsdb.Row) {
	f.t.Helper()
	ru := &ovsdb.RowUpdate2{}
	switch kind {
	case "initial":
		ru.Initial = &row
	case "insert":
		ru.Insert = &row
	case "modify":
		ru.Modify = &row
	}
	if err := f.tc.Populate2(ovsdb.TableUpdates2{table: {uuid: ru}}); err != nil {
		f.t.Fatal(err)
	}
}

// reconnect does what libovsdb does when it reconnects a monitor: purge the cache
// (no Delete events) and repopulate it from the server's current rows.
func (f *fakeClient) reconnect(table string, rows map[string]ovsdb.Row) {
	f.t.Helper()
	f.tc.Purge(f.dbModel)
	for uuid, row := range rows {
		f.apply(table, "initial", uuid, row)
	}
}

func newFakePair(t *testing.T) (*fakeClient, *fakeClient) {
	return newFakeClient(t, nbmodel.FullDatabaseModel, nbmodel.Schema),
		newFakeClient(t, sbmodel.FullDatabaseModel, sbmodel.Schema)
}

func switchNames(s *graph.Snapshot) []string {
	var names []string
	for _, ls := range s.LogicalSwitches {
		names = append(names, ls.Name)
	}
	sort.Strings(names)
	return names
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const (
	uuidA = "aaaaaaaa-0000-0000-0000-000000000001"
	uuidB = "bbbbbbbb-0000-0000-0000-000000000002"
	uuidC = "cccccccc-0000-0000-0000-000000000003"
	uuidR = "dddddddd-0000-0000-0000-000000000004"
)

// TestSyncerFollowsReconnect: rows removed while the daemon was disconnected must
// disappear once it reconnects. libovsdb purges and repopulates its cache without
// Delete events, so only a resync can drop them; readiness reflects the outage.
func TestSyncerFollowsReconnect(t *testing.T) {
	nb, sb := newFakePair(t)
	nb.apply("Logical_Switch", "initial", uuidA, ovsdb.Row{"name": "ls-a"})
	nb.apply("Logical_Switch", "initial", uuidB, ovsdb.Row{"name": "ls-b"})

	store := graph.NewStore(nil)
	s := NewSyncer(nb, sb, store, nil)
	s.healthEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Start(ctx) }()

	eventually(t, "initial snapshot", func() bool {
		return store.IsReady() && reflect.DeepEqual(switchNames(store.Current()), []string{"ls-a", "ls-b"})
	})

	// Incremental update, including a table that used to be ignored (routes).
	nb.apply("Logical_Switch", "insert", uuidC, ovsdb.Row{"name": "ls-c"})
	nb.apply("Logical_Router_Static_Route", "insert", uuidR, ovsdb.Row{"ip_prefix": "0.0.0.0/0", "nexthop": "10.0.0.1"})
	eventually(t, "live insert of a switch and a static route", func() bool {
		return len(store.Current().LogicalSwitches) == 3 && store.Current().StaticRoutes[uuidR] != nil
	})

	// Outage: not ready.
	nb.setDown(true)
	eventually(t, "not ready during the outage", func() bool { return !store.IsReady() })

	// While disconnected, ls-b was deleted. The reconnect repopulates without it.
	nb.reconnect("Logical_Switch", map[string]ovsdb.Row{
		uuidA: {"name": "ls-a"},
		uuidC: {"name": "ls-c"},
	})
	rev := store.Current().Revision
	nb.setDown(false)
	eventually(t, "ghost row dropped after reconnect", func() bool {
		return store.IsReady() && reflect.DeepEqual(switchNames(store.Current()), []string{"ls-a", "ls-c"})
	})
	if store.Current().Revision <= rev {
		t.Errorf("a resync must advance the revision")
	}
}

// TestSyncerOverflowResyncs: a dropped event triggers a resync, and stale queued
// events are discarded instead of being replayed over the rebuilt snapshot.
func TestSyncerOverflowResyncs(t *testing.T) {
	nb, sb := newFakePair(t)
	nb.apply("Logical_Switch", "initial", uuidA, ovsdb.Row{"name": "new-name"})

	store := graph.NewStore(nil)
	s := NewSyncer(nb, sb, store, nil)
	s.eventsChan = make(chan dbEvent, 1)
	// The queued event is older than the cache, and the newer one is dropped.
	s.enqueueEvent("Logical_Switch", eventUpdate, &nbmodel.LogicalSwitch{UUID: uuidA, Name: "old-name"})
	s.enqueueEvent("Logical_Switch", eventUpdate, &nbmodel.LogicalSwitch{UUID: uuidA, Name: "new-name"})
	if len(s.resyncCh) != 1 {
		t.Fatalf("an overflow must request a resync")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.nbUp.Store(true)
	s.sbUp.Store(true)
	go s.processEvents(ctx)
	eventually(t, "resync after overflow", func() bool { return store.IsReady() })
	time.Sleep(60 * time.Millisecond) // a few flush ticks: nothing stale may be replayed
	if got := switchNames(store.Current()); !reflect.DeepEqual(got, []string{"new-name"}) {
		t.Errorf("stale queued event replayed over the resync: %v", got)
	}
}

// TestSyncerRebuildPublishesNothingOnError: a table that cannot be read fails the
// rebuild instead of serving an empty or partial graph.
func TestSyncerRebuildPublishesNothingOnError(t *testing.T) {
	nb, sb := newFakePair(t)
	nb.apply("Logical_Switch", "initial", uuidA, ovsdb.Row{"name": "ls-a"})
	store := graph.NewStore(nil)
	s := NewSyncer(nb, sb, store, nil)
	if err := s.rebuildFullSnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	rev := store.Current().Revision

	err := errors.New("boom")
	sb.listErr.Store(&err)
	if err := s.rebuildFullSnapshot(context.Background()); err == nil {
		t.Fatalf("a failed table read must fail the rebuild")
	}
	if store.Current().Revision != rev || len(store.Current().LogicalSwitches) != 1 {
		t.Errorf("a failed rebuild must not publish a snapshot")
	}
}
