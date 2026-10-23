package ovsdb

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coulof/ovn-topology/pkg/graph"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
	"github.com/coulof/ovn-topology/pkg/ovsdb/sbmodel"
	"github.com/ovn-kubernetes/libovsdb/cache"
	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
)

type eventType int

const (
	eventAdd eventType = iota
	eventUpdate
	eventDelete
)

type dbEvent struct {
	table     string
	eventType eventType
	m         model.Model
}

// Syncer manages OVSDB monitors for NB and SB, maintaining an up-to-date graph store.
//
// The libovsdb cache is the source of truth; the snapshot follows it through cache
// events. Whenever events may have been missed the snapshot is rebuilt from the cache
// (a resync): after a reconnect, because libovsdb purges and repopulates its cache
// without emitting Delete events for rows removed while disconnected, and after the
// event queue overflowed.
type Syncer struct {
	nbClient client.Client
	sbClient client.Client
	store    *graph.Store
	logger   *slog.Logger

	// healthEvery is the period of the Echo probe that detects outages and reconnects.
	healthEvery time.Duration

	eventsChan chan dbEvent
	resyncCh   chan string // reason; buffered 1, requests coalesce
	nbUp       atomic.Bool
	sbUp       atomic.Bool
	closeOnce  sync.Once
	stopCh     chan struct{}
}

// NewSyncer creates a Syncer connecting NB and SB monitors to the graph store.
func NewSyncer(nbClient, sbClient client.Client, store *graph.Store, logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		nbClient:    nbClient,
		sbClient:    sbClient,
		store:       store,
		logger:      logger,
		healthEvery: 2 * time.Second,
		eventsChan:  make(chan dbEvent, 1024),
		resyncCh:    make(chan string, 1),
		stopCh:      make(chan struct{}),
	}
}

// Start connects to NB and SB, starts monitors, and blocks until ctx is canceled.
func (s *Syncer) Start(ctx context.Context) error {
	s.logger.Info("connecting to ovn databases",
		"nb_endpoints", s.nbClient.CurrentEndpoint(),
		"sb_endpoints", s.sbClient.CurrentEndpoint())

	// Connect NB
	if err := s.nbClient.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to OVN NB: %w", err)
	}
	// Connect SB
	if err := s.sbClient.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to OVN SB: %w", err)
	}

	// Register cache event handlers before starting monitors. libovsdb keeps the same
	// cache (and so these handlers) across reconnects when reconnect is enabled.
	s.registerHandlers()

	// Start monitors
	if _, err := s.nbClient.MonitorAll(ctx); err != nil {
		return fmt.Errorf("failed to monitor OVN NB: %w", err)
	}
	if _, err := s.sbClient.MonitorAll(ctx); err != nil {
		return fmt.Errorf("failed to monitor OVN SB: %w", err)
	}
	s.nbUp.Store(true)
	s.sbUp.Store(true)

	// Perform initial full rebuild from monitor caches
	if err := s.rebuildFullSnapshot(ctx); err != nil {
		return fmt.Errorf("initial graph snapshot build failed: %w", err)
	}

	s.store.SetReady(true)
	s.logger.Info("initial ovn topology snapshot synchronized successfully")

	// Start event loop for incremental updates and the health probe
	go s.processEvents(ctx)
	go s.watchHealth(ctx)

	<-ctx.Done()
	s.Close()
	return ctx.Err()
}

// Close disconnects clients and shuts down event processing.
func (s *Syncer) Close() {
	s.closeOnce.Do(func() {
		close(s.stopCh)
		s.nbClient.Close()
		s.sbClient.Close()
	})
}

func (s *Syncer) registerHandlers() {
	handler := &cache.EventHandlerFuncs{
		AddFunc: func(table string, m model.Model) {
			s.enqueueEvent(table, eventAdd, m)
		},
		UpdateFunc: func(table string, _, newModel model.Model) {
			s.enqueueEvent(table, eventUpdate, newModel)
		},
		DeleteFunc: func(table string, m model.Model) {
			s.enqueueEvent(table, eventDelete, m)
		},
	}
	s.nbClient.Cache().AddEventHandler(handler)
	s.sbClient.Cache().AddEventHandler(handler)
}

func (s *Syncer) enqueueEvent(table string, et eventType, m model.Model) {
	select {
	case s.eventsChan <- dbEvent{table: table, eventType: et, m: m}:
	default:
		s.requestResync("event queue overflow")
	}
}

// requestResync asks the event loop to rebuild the snapshot from the caches.
func (s *Syncer) requestResync(reason string) {
	select {
	case s.resyncCh <- reason:
	default: // one is already pending
	}
}

func (s *Syncer) processEvents(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var batch []dbEvent
	pending := "" // reason of a resync that has not succeeded yet

	flush := func() {
		if len(batch) == 0 {
			return
		}
		events := batch
		batch = nil

		s.store.Apply(func(draft *graph.Snapshot) []string {
			changedMap := make(map[string]struct{})
			for _, ev := range events {
				changedIDs := applyEventToDraft(draft, ev)
				for _, id := range changedIDs {
					changedMap[id] = struct{}{}
				}
			}
			result := make([]string, 0, len(changedMap))
			for id := range changedMap {
				result = append(result, id)
			}
			return result
		})
	}

	resync := func(reason string) {
		// Queued events are older than the cache: replaying them after the rebuild
		// could regress a row whose newer event was dropped. Events emitted from
		// here on describe changes the rebuild already sees or newer ones.
		batch = nil
		for drained := false; !drained; {
			select {
			case <-s.eventsChan:
			default:
				drained = true
			}
		}
		if err := s.rebuildFullSnapshot(ctx); err != nil {
			s.logger.Error("graph resync failed, will retry", "reason", reason, "error", err)
			s.store.SetReady(false)
			pending = reason
			return
		}
		pending = ""
		s.store.SetReady(s.nbUp.Load() && s.sbUp.Load())
		s.logger.Info("graph resynchronized from ovsdb caches", "reason", reason)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case reason := <-s.resyncCh:
			resync(reason)
		case ev := <-s.eventsChan:
			batch = append(batch, ev)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
			if pending != "" && s.nbUp.Load() && s.sbUp.Load() {
				resync(pending)
			}
		}
	}
}

// watchHealth probes both databases with Echo. libovsdb reconnects on its own and
// emits no notification when reconnect is enabled, so an Echo failure is how an
// outage is seen (not ready), and the first success after it is how the reconnect
// is seen (resync).
func (s *Syncer) watchHealth(ctx context.Context) {
	ticker := time.NewTicker(s.healthEvery)
	defer ticker.Stop()

	probe := func(name string, c client.Client, up *atomic.Bool) {
		pctx, cancel := context.WithTimeout(ctx, s.healthEvery)
		err := c.Echo(pctx)
		cancel()
		switch {
		case err != nil && up.Load():
			up.Store(false)
			s.store.SetReady(false)
			s.logger.Warn("ovn database unreachable, awaiting reconnect", "db", name, "error", err)
		case err == nil && !up.Load():
			up.Store(true)
			s.logger.Info("ovn database reconnected", "db", name)
			s.requestResync(name + " reconnected")
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			probe("nb", s.nbClient, &s.nbUp)
			probe("sb", s.sbClient, &s.sbUp)
		}
	}
}

// lister reads every row of a table into result, a pointer to a slice of models
// (the shape of client.Client.List).
type lister func(ctx context.Context, result any) error

// listInto reads every row of the model's table into m.
func listInto[T any](ctx context.Context, list lister, m map[string]*T, key func(*T) []string) error {
	var rows []T
	if err := list(ctx, &rows); err != nil {
		return fmt.Errorf("list %T: %w", rows, err)
	}
	for i := range rows {
		for _, k := range key(&rows[i]) {
			m[k] = &rows[i]
		}
	}
	return nil
}

// fillSnapshot reads every monitored NB and SB table into the entity maps of next.
func fillSnapshot(ctx context.Context, nb, sb lister, next *graph.Snapshot) error {
	errs := []error{
		listInto(ctx, nb, next.LogicalRouters, func(r *nbmodel.LogicalRouter) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.LogicalRouterPorts, func(r *nbmodel.LogicalRouterPort) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.StaticRoutes, func(r *nbmodel.LogicalRouterStaticRoute) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.Policies, func(r *nbmodel.LogicalRouterPolicy) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.NATs, func(r *nbmodel.NAT) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.GatewayChassis, func(r *nbmodel.GatewayChassis) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.HAChassisGroups, func(r *nbmodel.HAChassisGroup) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.HAChassis, func(r *nbmodel.HAChassis) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.LogicalSwitches, func(r *nbmodel.LogicalSwitch) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.LogicalSwitchPorts, func(r *nbmodel.LogicalSwitchPort) []string { return []string{r.UUID} }),
		listInto(ctx, nb, next.LoadBalancers, func(r *nbmodel.LoadBalancer) []string { return []string{r.UUID} }),
		listInto(ctx, sb, next.Chassis, func(r *sbmodel.Chassis) []string { return []string{r.Name, r.UUID} }),
		listInto(ctx, sb, next.Encaps, func(r *sbmodel.Encap) []string { return []string{r.UUID} }),
		listInto(ctx, sb, next.PortBindings, func(r *sbmodel.PortBinding) []string { return []string{r.UUID} }),
		listInto(ctx, sb, next.DatapathBindings, func(r *sbmodel.DatapathBinding) []string { return []string{r.UUID} }),
		listInto(ctx, sb, next.BFDs, func(r *sbmodel.BFD) []string { return []string{r.UUID} }),
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// rebuildFullSnapshot replaces the snapshot with the content of both caches. It
// publishes nothing if any table cannot be read, so a partial graph is never served.
func (s *Syncer) rebuildFullSnapshot(ctx context.Context) error {
	next := graph.NewEmptySnapshot(0, nil)
	if err := fillSnapshot(ctx, s.nbClient.List, s.sbClient.List, next); err != nil {
		return err
	}

	s.store.Apply(func(draft *graph.Snapshot) []string {
		draft.LogicalRouters = next.LogicalRouters
		draft.LogicalRouterPorts = next.LogicalRouterPorts
		draft.StaticRoutes = next.StaticRoutes
		draft.Policies = next.Policies
		draft.NATs = next.NATs
		draft.GatewayChassis = next.GatewayChassis
		draft.HAChassisGroups = next.HAChassisGroups
		draft.HAChassis = next.HAChassis
		draft.LogicalSwitches = next.LogicalSwitches
		draft.LogicalSwitchPorts = next.LogicalSwitchPorts
		draft.LoadBalancers = next.LoadBalancers
		draft.Chassis = next.Chassis
		draft.Encaps = next.Encaps
		draft.PortBindings = next.PortBindings
		draft.DatapathBindings = next.DatapathBindings
		draft.BFDs = next.BFDs
		return []string{"*"}
	})
	return nil
}

// upsert applies an add, update or delete of one row to a snapshot table.
func upsert[T any](m map[string]*T, key string, v *T, et eventType) {
	if et == eventDelete {
		delete(m, key)
	} else {
		m[key] = v
	}
}

func applyEventToDraft(draft *graph.Snapshot, ev dbEvent) []string {
	et := ev.eventType
	switch val := ev.m.(type) {
	case *nbmodel.LogicalRouter:
		upsert(draft.LogicalRouters, val.UUID, val, et)
		return []string{graph.PrefixLR + val.UUID}
	case *nbmodel.LogicalRouterPort:
		upsert(draft.LogicalRouterPorts, val.UUID, val, et)
		return []string{graph.PrefixLRP + val.UUID}
	case *nbmodel.LogicalSwitch:
		upsert(draft.LogicalSwitches, val.UUID, val, et)
		return []string{graph.PrefixLS + val.UUID}
	case *nbmodel.LogicalSwitchPort:
		upsert(draft.LogicalSwitchPorts, val.UUID, val, et)
		return []string{graph.PrefixLSP + val.UUID}
	case *nbmodel.NAT:
		upsert(draft.NATs, val.UUID, val, et)
		return []string{graph.PrefixNAT + val.UUID}
	case *sbmodel.Chassis:
		upsert(draft.Chassis, val.Name, val, et)
		upsert(draft.Chassis, val.UUID, val, et)
		return []string{graph.PrefixChassis + val.Name}
	case *sbmodel.PortBinding:
		upsert(draft.PortBindings, val.UUID, val, et)
		return []string{graph.PrefixLSP + val.LogicalPort}

	// Rows below have no node of their own: they feed router counts, gateway roles,
	// chassis encaps and datapath joins, so the change is reported as "everything".
	case *nbmodel.LogicalRouterStaticRoute:
		upsert(draft.StaticRoutes, val.UUID, val, et)
	case *nbmodel.LogicalRouterPolicy:
		upsert(draft.Policies, val.UUID, val, et)
	case *nbmodel.GatewayChassis:
		upsert(draft.GatewayChassis, val.UUID, val, et)
	case *nbmodel.HAChassisGroup:
		upsert(draft.HAChassisGroups, val.UUID, val, et)
	case *nbmodel.HAChassis:
		upsert(draft.HAChassis, val.UUID, val, et)
	case *nbmodel.LoadBalancer:
		upsert(draft.LoadBalancers, val.UUID, val, et)
	case *sbmodel.Encap:
		upsert(draft.Encaps, val.UUID, val, et)
	case *sbmodel.DatapathBinding:
		upsert(draft.DatapathBindings, val.UUID, val, et)
	case *sbmodel.BFD:
		upsert(draft.BFDs, val.UUID, val, et)
	default:
		return nil
	}
	return []string{"*"}
}
