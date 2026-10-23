# AGENTS.md — ovn-topology

Instructions for coding agents working in this repository. Read fully before making changes.

Companion documents, to read before starting work:
- `ROADMAP.md` — milestones, scope, Definition of Done, testing strategy, open questions. Work only on the current milestone.
- `docs/adr/` — architecture decisions. ADR-0001 (privilege tiers) governs what code may live where.

## 1. What this project is

A **read-only network topology visualizer for OVN as deployed by Kube-OVN**, made of:

- `ovn-topologyd` — Go daemon that monitors the OVN Northbound (NB) and Southbound (SB) databases via OVSDB, builds a joined in-memory graph, and serves it over HTTP JSON + SSE.
- `ovn-topo` — Go TUI (Bubble Tea) consuming the same graph package.
- Rancher UI Extension — **paused** (see §7). The code is archived at tag `archive/ui-extension`; it is not in the tree.

Targets: Rancher-managed RKE2 and Rancher-managed Harvester clusters running Kube-OVN. Latest Rancher only.

### Hard rules (non-negotiable)

1. **Read-only.** Nothing in this repo writes to OVSDB or to the Kubernetes API. The only permitted write is the daemon's own history store (ADR-0002). No `Create`, `Update`, `Mutate`, `Delete`, or `Transact` with write ops on any libovsdb client. Enforced by `golangci-lint` (`forbidigo`) — do not disable or work around it.
2. **OVSDB is the source of truth for topology.** Do **not** read Kube-OVN CRDs (Vpc, Subnet, IP, Ovn*, Iptables*, SecurityGroup…). Kubernetes identity comes from NB `external_ids`. Allowed Kubernetes reads (get/list/watch only) are exactly: `Node`, `Pod`, `Service`, `EndpointSlice`, KubeVirt `VirtualMachineInstance`/`VirtualMachine`, `NetworkAttachmentDefinition`. They are used to enrich graph nodes, never to create them.
3. **Metrics are optional overlays only**, never a source of topology.
4. **Air-gap friendly.** No runtime calls to the internet. No CDN assets in the UI. All images and charts must be mirrorable (Hauler).
5. **Privilege tiers (ADR-0001).** T0 (passive) code may live anywhere. T1 (exec-based diagnostics) lives only in `pkg/diag` and is used only by `cmd/ovn-topo`. The daemon and `pkg/mcp` must never depend on `pkg/diag` or `remotecommand`; MCP exposes T0 tools only — enforced by `depguard` and `hack/check-deps.sh`. Commands run by `pkg/diag` come from a typed allowlist; never build commands from free-form strings.

## 2. Repository layout

```
ovn-topology/
├── go.mod                   # module root; tags are plain vX.Y.Z
├── api/openapi.yaml         # API contract — single source of truth
├── pkg/
│   ├── ovsdb/               # libovsdb clients, generated NB/SB models, monitors
│   │   ├── nbmodel/         # generated — do not edit
│   │   └── sbmodel/         # generated — do not edit
│   ├── graph/               # node/edge model, NB↔SB joins, views, aggregation
│   ├── api/                 # generated Go types from openapi.yaml — do not edit
│   ├── server/              # HTTP + SSE handlers
│   ├── trace/               # T0 configuration-based path computation (M5)
│   ├── history/             # T0 recorder, stores (memory/bbolt), reconstructor, summarizer (ADR-0002, M8)
│   ├── mcp/                 # T0 MCP tools for `ovn-topo mcp` (M9) — never imports pkg/diag
│   └── diag/                # T1 exec-based diagnostics — TUI only (ADR-0001, M6)
├── cmd/
│   ├── ovn-topologyd/
│   └── ovn-topo/
├── charts/ovn-topology/     # daemon Helm chart (installed per downstream cluster)
├── testdata/                # OVSDB snapshots used as fixtures
├── test/                    # e2e (Go, tag e2e), ovn/ (raw OVSDB) — see ROADMAP testing strategy
├── hack/                    # codegen, check-deps.sh, capture-fixtures.sh, kind/, harvester/
├── docs/adr/                # architecture decision records
├── ROADMAP.md
└── .github/workflows/       # go.yml, cli.yml, image.yml, release.yml
```

Generated code lives in dedicated directories and is regenerated with `make generate`. Never hand-edit it.

## 3. Commands

```
make generate      # OVSDB models (modelgen) + Go/TS types from api/openapi.yaml
make lint          # golangci-lint (incl. forbidigo read-only rule) + ADR-0001 deps check
make test          # go test ./... (unit + in-memory OVSDB tests)
make build         # daemon + TUI binaries
make image         # daemon container image (distroless, non-root)
make dev-cluster   # hack/: kind or lima cluster with Kube-OVN + sample VPCs
```

Run `make generate lint test` before declaring any task done. If you change `api/openapi.yaml`, regenerate and update the Go consumers in the same change.

## 4. Backend (`pkg/`, `cmd/ovn-topologyd`)

### OVSDB access

- Library: **`github.com/ovn-kubernetes/libovsdb`** (the maintained home; the old `github.com/ovn-org/libovsdb` path must not be used). Packages used: `client`, `model`, `cache`, `modelgen`, and `server` + `database` for tests.
- Models: generate with libovsdb `modelgen` from the OVN `ovn-nb.ovsschema` / `ovn-sb.ovsschema` of the targeted OVN version (pinned in `hack/ovn-version`). Do **not** import `github.com/kubeovn/kube-ovn` — the dependency tree is huge. Only generate tables we use (see §5).
- Use **monitors** (push updates), never polling.
- Connect with **all raft endpoints** and `leader_only=false` so reads hit followers.
- Endpoints, TLS (client cert/key/CA from a Secret), and namespace are configurable. Autodetect default: services `ovn-nb` / `ovn-sb` in `kube-system`. **Harvester's Kube-OVN addon layout is unverified** — do not hardcode; keep it configurable and log what was detected.
- Reconnect with backoff; on reconnect, rebuild the graph from a fresh monitor snapshot.

### Graph

- The graph is rebuilt incrementally from monitor updates; the HTTP layer reads immutable snapshots (copy-on-write or versioned snapshot behind an `atomic.Pointer`). No locks held during request serialization.
- Each snapshot has a monotonically increasing `revision`.
- Node IDs are stable and typed: `<type>:<nb-or-sb-uuid>` (e.g. `lr:7f3c…`, `chassis:…`). External/synthetic nodes use `ext:<provider-network>`.

### HTTP API

Defined in `api/openapi.yaml`. Base path `/api/v1`.

| Endpoint | Purpose |
|---|---|
| `GET /graph?view=l3\|l2\|chassis&focus=<id>&depth=<n>` | Aggregated graph for a view |
| `GET /node/{id}` | Full detail for one node (all columns, external_ids, related objects) |
| `GET /chassis` | Chassis list with encaps, binding counts, gateway roles |
| `GET /events` | SSE stream of `{revision, changed: [ids]}` |
| `GET /healthz`, `/readyz` | Ready only after initial NB+SB snapshot |
| `GET /version` | Daemon + API version, OVN schema versions |

- **Server-side aggregation is mandatory**: never send unbounded LSP lists in `/graph`. LS nodes carry port counts; individual LSPs only appear in `view=l2&focus=ls:<id>`, paginated.
- SSE must degrade: if the stream breaks (Rancher proxy buffering is possible), the UI falls back to polling `/graph` with `If-None-Match: <revision>`.

### Security

- Distroless, non-root, read-only root filesystem.
- K8s RBAC for the daemon ServiceAccount: `get/list/watch` on exactly the kinds allowed by hard rule 2 (`nodes`, `pods`, `services`, `endpointslices`, KubeVirt `virtualmachines`/`virtualmachineinstances`, `network-attachment-definitions`), plus `get` on the OVN TLS secret only. Nothing else.
- Access to the API is via the Kubernetes service proxy; viewer authorization is `services/proxy` RBAC. Document this in the chart README.
- OVSDB NB has no read-only role; read-only is guaranteed by code + lint. Say so in the README. Do not soften it.

## 5. Data model

### NB tables used
`Logical_Router`, `Logical_Router_Port`, `Logical_Router_Static_Route`, `Logical_Router_Policy`, `NAT`, `Gateway_Chassis`, `HA_Chassis_Group`, `HA_Chassis`, `Logical_Switch`, `Logical_Switch_Port`, `Load_Balancer`, `ACL`, `Port_Group`, `Address_Set` (the last three are consumed from M4).

### SB tables used
`Chassis`, `Encap`, `Port_Binding`, `Datapath_Binding`, `BFD` (M4).

### Joins
- LSP(type=`router`).`options:router-port` → LRP name → **`router-peer`** edge.
- `Port_Binding.logical_port` == LSP/LRP name → binding state + chassis.
- `Datapath_Binding.external_ids["logical-switch"|"logical-router"]` == NB UUID.
- Active gateway = `Port_Binding.chassis` of the `chassisredirect` port (`cr-<lrp>`).
- Chassis ↔ k8s Node: node annotation `ovn.kubernetes.io/chassis` == `Chassis.name` (the OVS system-id, authoritative), fallback to `Chassis.hostname`. Hostname is informational in OVN and can be shared or stale; report which rule matched.
- Kubernetes identity from NB `external_ids`: LSP `pod`, `namespace`, `ls`; `vendor=kube-ovn`; LR VPC name. Treat missing keys as normal (non-Kube-OVN objects exist).

### Node types
`LR, LS, LRP, LSP, Chassis, NAT, Route, Policy, LB, HAChassisGroup, External`
From M2/M4: `Workload` (Pod / VM, with owner), `Node`, `Service`, `ACL`, `PortGroup`, `AddressSet`.

Additional edge types introduced by later milestones (`attached-to` LSP→Workload, `runs-on` Chassis→Node, `member-of` LSP→PortGroup, `applies-to` ACL→PortGroup/LS, `references` ACL→AddressSet, `backend` LB→LSP, `exposes` Service→LB, `resolves-to` IP→owner) are specified in `ROADMAP.md` per milestone. Add them to `api/openapi.yaml` and to this table in the same PR.

### Edge types
| Type | From → To | Source |
|---|---|---|
| `contains` | LR→LRP, LS→LSP | NB |
| `router-peer` | LRP ↔ LSP(router) | NB |
| `bound-on` | LSP/LRP → Chassis | SB |
| `gateway` | LRP → Chassis (attr: priority, active) | NB + SB |
| `nat-target` | NAT → LSP (distributed FIP) | NB |
| `localnet` | LSP(localnet) → External | NB |

### Known traps (must be reflected in UI text, not hidden)
- **VPC NAT Gateway (`Iptables*`) NAT is iptables inside a pod, not OVN NAT** — it never appears in the NB `NAT` table.
- Default VPC `natOutgoing` in centralized mode is an LR policy reroute + node iptables, not NB `NAT`.
- An LS can attach to multiple LRs (e.g. `join`). Don't assume a tree.

## 6. Views (shared semantics, TUI and UI)

1. **L3 (default)**: LR and LS nodes; VPC = compound node around an LR and its attached LSs; edges = `router-peer` labelled with LRP networks; NAT/route/policy/LB as counts on LR; localnet to External; gateway LRPs show chassis + priority, active one highlighted.
2. **L2** (focus on an LS): LSPs grouped by namespace or by chassis; type, MAC/IP, binding badge (bound on X / **unbound**).
3. **Chassis**: chassis with encap IP/type, binding counts, gateway roles and HA group priorities.
4. **Path** (M5, `pkg/trace`, T0): src LSP → dst LSP/IP/VIP, computed from NB configuration and always labelled "approximate". Real `ovn-trace` packet traces are T1 and TUI-only (M6, `pkg/diag`).

## 7. UI extension — paused

The Rancher UI Extension (Vue 3, `@rancher/shell`, elkjs + SVG) is **paused** and not in the tree. Its code, its CI workflow and the full instructions that were in this section are archived at tag `archive/ui-extension`:

```
git checkout archive/ui-extension -- ui .github/workflows/ui.yml   # restore the code
git show archive/ui-extension:AGENTS.md                              # the full §7/§8 UI rules
```

Do not start UI work without an explicit task. The daemon API (`api/openapi.yaml`, SSE with heartbeat, `If-None-Match` polling) stays UI-ready; restoring the extension also means restoring `make ui-*`, the ECI release job and the CI type-drift check (`hack/generate.sh` already generates TS types when `ui/pkg/ovn-topology` exists).

## 8. Packaging

- Daemon chart `charts/ovn-topology`: installed on **each** downstream cluster running Kube-OVN.
- Tag `vX.Y.Z`: Go module, daemon image, daemon chart and the `ovn-topologyd`/`ovn-topo` binaries. `release.yml` produces them from one version bump; never hand-craft them.
- Air-gap: release produces a Hauler manifest listing the daemon image and chart.
- While the extension is paused, its tags (`ovn-topology-X.Y.Z` chart, `ovn-topology-ui-X.Y.Z` ECI) are not produced; the lockstep rules are in `archive/ui-extension`.

## 9. Testing

- Unit tests for every join and view in `pkg/graph` using fixtures.
- Fixtures: OVSDB snapshots in `testdata/` (captured with `ovsdb-client backup` from a dev cluster), loaded into libovsdb's in-process test server (`github.com/ovn-kubernetes/libovsdb/server`) so tests exercise the real monitor path.
- Golden-file tests for `/graph` responses per view (`-update` flag to regenerate).
- Required fixture scenarios: default VPC only; custom VPC with distributed FIP; centralized gateway with HA chassis group; underlay/localnet; LS attached to multiple LRs; unbound port; chassis down.

## 10. Conventions

- Go: latest stable, `log/slog` structured logging, context-aware everywhere, no global state except metrics registry.
- Errors wrap with `%w`; no panics outside `main`.
- Commits: Conventional Commits.
- Don't add dependencies without a reason stated in the PR description.

## 11. Delivery

Milestones, scope and Definition of Done live in `ROADMAP.md`. Do not start a milestone before the previous one meets its DoD. Do not implement backlog items without an explicit task. Record answers to open questions in `ROADMAP.md` with evidence.
