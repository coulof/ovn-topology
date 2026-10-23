package ovsdb

import (
	"context"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/coulof/ovn-topology/pkg/ovsdb/nbmodel"
	"github.com/coulof/ovn-topology/pkg/ovsdb/sbmodel"
	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
)

// NewNBClient creates a read-only libovsdb client for OVN Northbound database.
func NewNBClient(ctx context.Context, cfg Config) (client.Client, error) {
	dbModel, err := nbmodel.FullDatabaseModel()
	if err != nil {
		return nil, fmt.Errorf("failed to load NB database model: %w", err)
	}

	return createClient(ctx, dbModel, cfg)
}

// NewSBClient creates a read-only libovsdb client for OVN Southbound database.
func NewSBClient(ctx context.Context, cfg Config) (client.Client, error) {
	dbModel, err := sbmodel.FullDatabaseModel()
	if err != nil {
		return nil, fmt.Errorf("failed to load SB database model: %w", err)
	}

	return createClient(ctx, dbModel, cfg)
}

func createClient(_ context.Context, dbModel model.ClientDBModel, cfg Config) (client.Client, error) {
	var opts []client.Option

	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("no ovsdb endpoints provided")
	}

	for _, ep := range cfg.Endpoints {
		opts = append(opts, client.WithEndpoint(ep))
	}

	// Read-only access hitting followers per AGENTS.md
	opts = append(opts, client.WithLeaderOnly(false))

	tlsConfig, err := cfg.BuildTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to configure TLS: %w", err)
	}
	if tlsConfig != nil {
		opts = append(opts, client.WithTLSConfig(tlsConfig))
	}

	// Configure exponential backoff for reconnects
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 10 * time.Second
	b.MaxElapsedTime = 0 // Never give up reconnecting

	reconnectTimeout := cfg.ReconnectTimeout
	if reconnectTimeout <= 0 {
		reconnectTimeout = 10 * time.Second
	}
	if cfg.InactivityCheck > reconnectTimeout {
		// Echo probes detect half-open connections (a raft member rebooted without
		// a TCP reset) and force a reconnect; it implies WithReconnect.
		opts = append(opts, client.WithInactivityCheck(cfg.InactivityCheck, reconnectTimeout, b))
	} else {
		opts = append(opts, client.WithReconnect(reconnectTimeout, b))
	}

	ovsClient, err := client.NewOVSDBClient(dbModel, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize libovsdb client: %w", err)
	}

	return ovsClient, nil
}
