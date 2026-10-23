# ovn-topology

A read-only network topology visualizer for OVN as deployed by Kube-OVN on Rancher-managed RKE2 and Harvester clusters.

## Components

- **`ovn-topologyd`**: Go daemon that connects to OVN Northbound (NB) and Southbound (SB) databases over OVSDB, monitors updates, maintains a joined in-memory network graph, and serves it over HTTP JSON and SSE.
- **`ovn-topo`**: Terminal UI (Bubble Tea) that renders an L3 topology tree and an interconnection matrix of router ports directly in your terminal.
- **Rancher UI Extension**: paused; archived at tag `archive/ui-extension` (see below).
- **`charts/ovn-topology/`**: Helm chart deployed once per downstream cluster.

## Design and Security Rules

- **Strictly read-only**: OVSDB does not have a read-only user role. Read-only access is guaranteed in code and enforced during build by `golangci-lint` using `forbidigo`. The codebase contains no write operations (`Create`, `Update`, `Mutate`, `Delete`, `Transact`) against OVSDB or the Kubernetes API.
- **OVSDB is the source of truth**: The daemon does not read Kube-OVN CRDs (such as Vpc, Subnet, or IP). Kubernetes metadata comes from OVN NB `external_ids`. The only Kubernetes resource read by the daemon is `Node`, used solely to map chassis hostnames to node names.
- **Access control**: API access goes through the Kubernetes service proxy (`/api/v1/namespaces/<namespace>/services/http:ovn-topologyd:8080/proxy/api/v1/...`). Authorization is controlled by standard `services/proxy` RBAC rules.
- **Air-gap friendly**: No runtime calls to the internet, no CDN dependencies, and non-root distroless container images.

## Architecture

```text
  Downstream Cluster (RKE2 / Harvester)
  ┌────────────────────────────────────────────────────────────┐
  │  OVN Northbound (6641)        OVN Southbound (6642)        │
  │           ▲                              ▲                 │
  │           │ OVSDB push monitor           │ OVSDB push      │
  │           │ (leader_only=false)          │ monitor         │
  │  ┌────────┴──────────────────────────────┴──────────────┐  │
  │  │ ovn-topologyd                                        │  │
  │  │   • In-memory joined graph (atomic snapshots)        │  │
  │  │   • K8s NodeMapper (chassis <-> node)                │  │
  │  │   • HTTP REST + SSE API (:8080)                      │  │
  │  └───────────────────────┬──────────────────────────────┘  │
  └──────────────────────────┼─────────────────────────────────┘
                             │
     ┌───────────────────────┴───────────────────────┐
     │                                               │
     ▼                                               ▼
Rancher UI Extension                         ovn-topo (TUI)
(via k8s service proxy)                      (Bubble Tea)
```

## Quick Start: Local Testing

To test against an existing cluster (for example `export KUBECONFIG=~/.kube/hv-01.yaml`):

```bash
# 1. Forward OVN database ports to localhost
mise run pf

# 2. Run the daemon locally (terminal 1)
mise run daemon

# 3. Test the API or launch the TUI (terminal 2)
mise run test-live
mise run tui
```

When finished, stop the port-forwarding:

```bash
mise run pf-stop
```

## TUI Controls (`ovn-topo`)

k9s-style navigation; see [`docs/tui-guide.md`](docs/tui-guide.md) for the full guide.

- **`1` - `4`** (or `:xray vpc`, `:xray chassis`, `:xray workload`, `:ports`): switch between the three xray trees and the router-port table.
- **`j`/`k`**, **`h`/`l`**, **`Space`**: move, fold/unfold, toggle. **`e`/`E`** expands/folds everything.
- **`Enter`** on a `⇢` relation (chassis, workload, peer port…) jumps to that object; **`b`** goes back.
- **`i`**: show/hide the right **details pane** (interconnections of the selection: router ports and peers, connected routers, path out of a subnet, gateways); the tree starts with two levels open.
- **`/`**: filter (keeps the ancestors of every match). **`d`** describe, **`y`** raw columns.
- **`x`**: open **Tier 1 Diagnostics** on the selection (interactive `ovn-trace` with fzf search & custom IP, live `tcpdump`, conntrack, `ovs-vsctl show`, controller logs).
- **`r`** refresh, **`L`** pause/resume live (SSE) updates, **`t`** cycle themes, **`?`** help, **`q`** quit.

Launch with Tier 1 diagnostics enabled:
```bash
mise run tui-diag
```

Launch with a preferred theme:
```bash
./bin/ovn-topo --theme=suse
./bin/ovn-topo --theme=dracula
./bin/ovn-topo --theme=nord
./bin/ovn-topo --theme=tokyonight
./bin/ovn-topo --theme=catppuccin
```

### Tier 1 Diagnostics Mode (ADR-0001)

Launch `ovn-topo` with `--diag` to enable read-only execution diagnostics in cluster pods (`ovn-central`, `kube-ovn-cni`, `ovs-ovn`):

```bash
# Launch with diagnostics enabled
./bin/ovn-topo --diag --kubeconfig=~/.kube/config

# Or using mise
mise run tui-diag
```

Available diagnostic actions from the TUI (`x` key):
1. **`ovn-trace`**: point-and-click microflow packet trace against `ovn-central` to inspect routing, ACLs, and NAT stage-by-stage.
2. **Conntrack dump**: dump connection tracking entries on the active chassis node.
3. **OVS interfaces**: inspect physical interfaces and port bindings via `ovs-vsctl`.
4. **Controller logs**: tail `ovn-controller` logs on the target node.

If RBAC denies `pods/exec`, the TUI header shows the `DIAG` badge as degraded with the reason and falls back to passive mode without crashing.

## Rancher UI Extension (paused)

The Rancher/Harvester UI extension is paused. Its code is archived at tag `archive/ui-extension` (`git checkout archive/ui-extension -- ui`). The daemon API stays usable by any client.

## Release Artifacts

Every version tag (`vX.Y.Z`) publishes:

| Artifact | Registry Path | Description |
|---|---|---|
| **Daemon Image** | `ghcr.io/coulof/ovn-topologyd:vX.Y.Z` | Multi-arch (`linux/amd64`, `linux/arm64`) distroless daemon |
| **Daemon Chart** | `oci://ghcr.io/coulof/charts/ovn-topology:X.Y.Z` | OCI Helm chart for downstream clusters |
| **Binaries** | GitHub Releases | Static `ovn-topologyd` + `ovn-topo` archives for Linux/macOS (amd64, arm64); `ovn-topo` zip for Windows (amd64, arm64) |

### Air-Gapped Environments (Hauler)

Each release includes a `hauler.yaml` manifest bundling the daemon image and Helm chart:

```bash
# Sync release artifacts into a local Hauler store
hauler store sync -f hauler.yaml

# Copy artifacts to an internal air-gapped registry
hauler store copy registry://registry.internal.example.com
```

## Development

Prerequisites: Go 1.24+, `golangci-lint`, `make`.

```bash
make generate    # Regenerate OVSDB models and OpenAPI Go types
make lint        # Run golangci-lint (enforcing forbidigo read-only check)
make test        # Run unit tests and golden-file comparisons
make build       # Build bin/ovn-topologyd and bin/ovn-topo
make image       # Build container image
```

## API Summary

Base path: `/api/v1`

| Endpoint | Method | Description |
|---|---|---|
| `/graph?view=l3` | GET | Aggregated L3 graph (VPCs, LRs, LSs, external networks) |
| `/graph?view=l2&focus=ls:<id>` | GET | Paginated L2 switch ports and chassis bindings |
| `/graph?view=chassis` | GET | Chassis list with encaps and binding counts |
| `/node/{id}` | GET | Full details and raw OVSDB columns for a node |
| `/chassis` | GET | Chassis details, HA groups, and gateway roles |
| `/events` | GET | SSE stream emitting graph revision changes |
| `/readyz` | GET | Readiness probe (returns 200 after initial DB sync) |
| `/healthz` | GET | Liveness probe |
| `/version` | GET | Daemon version and OVN schema versions |
