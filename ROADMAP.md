# ROADMAP — ovn-topology

This file is the delivery plan for coding agents. Read it together with `AGENTS.md` (rules, layout, conventions) and `docs/adr/` (decisions). Where this file and `AGENTS.md` disagree, `AGENTS.md` wins. Flag the conflict instead of guessing.

## How to use this roadmap

- Work milestone by milestone. **Do not start a milestone until the previous one meets its Definition of Done (DoD).** Milestones marked *parallel* are the exception.
- Each milestone lists **Scope**, **Out of scope**, **Deliverables**, **DoD** and **Tests**. Anything not in Scope is out of scope.
- Every feature carries a privilege tier (see ADR-0001):
  - **T0**: passive OVSDB/K8s reads, allowed everywhere.
  - **T1**: exec-based diagnostics, TUI only, `pkg/diag` only.
- Open questions (§ Open questions) block specific items. Resolve them with evidence (a test, a doc link, a lab run), record the answer in this file, then continue.
- When a milestone is done, tick its checkbox and add a one-line note with the date and PR link.

## Milestones overview

| # | Milestone | Tier | Consumers | Depends on |
|---|---|---|---|---|
| M0 | Foundations: repo, codegen, lint guards, CI, dev cluster, fixtures | — | — | — |
| M1 | Graph core + daemon L3 API | T0 | daemon | M0 |
| M2 | TUI: L3 / L2 / Chassis + workload identity | T0 | TUI | M1 |
| M3 | Rancher extension: L3 view, side panel, live updates — **paused 2026-10-23** | T0 | extension | M1 (*parallel* with M2) |
| M4 | Relationships: IP resolver, ACL/PG/AS, LB↔Service, gateway/BFD, migration, consistency checks | T0 | daemon, TUI, extension | M2, M3 |
| M5 | Configuration-based path computation | T0 | daemon, TUI, extension | M4 |
| M6 | TUI diagnostics: trace, tcpdump, wiring, conntrack, logs | T1 | TUI only | M4 |
| M7 | Harvester validation + release hardening | — | all | M3, M4 |
| M8 | Topology history & diff (ADR-0002) | T0 | daemon, TUI, extension | M4 (recorder may start after M1) |
| M9 | MCP server (T0 tools only) | T0 | AI agents via `ovn-topo mcp` | M5; history tools gated on M8 |
| Backlog | ACL hit counters, OVN-IC, metrics overlay, logical flow browser, history archival to S3, segmentation audit report, fleet view | mixed | — | — |

## Status and tech-debt register (2026-10-21)

**Order rule broken.** M2, M3 and M6 were built while the M0 and M1 DoD were still open, and M6 ahead of M4. This is recorded here rather than hidden. **Before M4 starts, the foundation items below take priority.** The M2 and M6 work stays, and new feature work waits.

| M | DoD | Reality |
|---|---|---|
| M0 | 0/4 | Lint and deps guards work. No `test/` tree, no `make dev-cluster` run recorded, no `hack/capture-fixtures.sh`. |
| M1 | 0/5 | API and goldens exist on a single hand-written fixture. The live data path was fixed and unit-tested on 2026-10-21 (see the M1 follow-up). The layer 2/3 tests, scale test and API freeze are missing. |
| M2 | 5/6 | LR routes/policies/NAT open. Scenario navigation was checked on the lab, not on committed fixtures. |
| M3 | 2/3 | **No live updates in the extension** (no SSE, no polling) and no API-version banner. See the M3 DoD. |
| M4, M5 | 0 | Not started. |
| M6 | 5/5 | Done out of order. Layer 3 verification was manual on `hv-01`; there is no automated suite. |
| M7–M9 | 0 | Not started. |

**Tech-debt register** (from the 2026-10-21 audit; fixed items moved to milestone follow-ups):

| # | Item | Where it closes |
|---|---|---|
| B1 | Only one hand-written fixture. `hack/capture-fixtures.sh` (2026-10-21) writes `testdata/scenarios/<name>/`, which `pkg/ovsdb.LoadFixture` decodes through the libovsdb cache, and `TestScenarioGoldens` renders each scenario. One real capture is committed: `testdata/scenarios/hv-01-lab` (2026-10-23, Kube-OVN v1.16.2, OVN 25.03.4). It is the whole lab as it was, not a scenario. **To do: recapture clean testdata**, one capture per named scenario (`overlay-default`, `custom-vpc-ovn-nat`, `kubevirt-multinic` first), each from a cluster prepared to contain exactly that scenario. Each capture's NB must differ from the others, or it is not a new scenario; record in its `meta.json` what was set up. The lab runs OVN 25.03.4 while `hack/ovn-version` pins v24.03.2 (Q4): align the pin with the captured version before relying on columns newer than 24.03. | M0 / M1 |
| B2 | No `test/` tree: layers 2–5 absent. | M0 / M1 / M7 |
| B3 | No scale test. `/graph` rebuilds the whole view per request, with no per-revision cache. | M1 |
| B4 | `make generate` output for `pkg/ovsdb/*model` differs between machines (`validate` tags), and CI skips the diff. | M0 |
| B5 | Extension has one unit test (`layout.test.ts`). | M3 (paused) |
| C1 | ~~`forbidigo` misses `.Patch`, `.Apply` and `.Evict`.~~ **Done 2026-10-23:** a type-aware rule forbids `Patch`/`Apply*`/`Evict*`/`DeleteCollection` on `k8s.io/client-go` types (scoped so `graph.Store.Apply` stays allowed). | done |
| C2 | ~~No `depguard` rule for `pkg/mcp` yet.~~ **Done 2026-10-23:** `depguard` and `hack/check-deps.sh` cover `pkg/mcp` (the script checks it once the package exists). | done |
| C3 | ~~OVN schemas are downloaded by `make generate`, not vendored.~~ **Not debt:** `hack/schema/*.ovsschema` are committed since the first commit; `make generate` only downloads when they are missing. | closed |
| A6 | ~~SSE has no heartbeat; idle streams may be cut by proxies.~~ **Done 2026-10-23:** `/events` sends a `: keepalive` comment every 15s. | done |
| E1 | `cmd/ovn-topo/diag_ui.go` (1.3k lines, one modal, flag-driven state). | before M5 |
| E2 | ~~Three chassis → node resolvers.~~ **Done 2026-10-23:** the TUI and `pkg/diag` use only the daemon's `nodeName` (`graph.MatchChassis`); the `ovn-sbctl list Chassis` exec at diag start and the client-side re-mapping in `pkg/diag` are gone. A chassis with no matched node falls back to its OVN hostname, and the diag target says so. | done |
| F2 | ~~CORS `*` on the daemon.~~ **Done 2026-10-23:** removed; the extension calls through the Rancher service proxy (same origin). | done |
| E3 | `ovn-trace` output parsed with regexes (`ofproto/trace` and `ovn-detrace` will add two more formats). | M5 / M6 detrace follow-up |
| F1 | Version `0.1.0` everywhere; no release has run end to end. | M7 |

---

## M0 — Foundations

**Goal:** an empty but fully wired repo, so every later milestone is "add code + fixtures + tests", never "fix plumbing".

**Scope**
- Repo layout per `AGENTS.md` §2, plus `pkg/diag/` (empty, with a package doc stating ADR-0001), `docs/adr/`, `test/` (see §Testing strategy).
- `make generate`:
  - libovsdb `modelgen` for NB/SB from the OVN schema pinned in `hack/ovn-version`, using only the tables listed in `AGENTS.md` §5 plus `ACL`, `Port_Group`, `Address_Set`, `BFD`.
  - `oapi-codegen` (Go) and `openapi-typescript` (UI) from `api/openapi.yaml`.
- golangci-lint:
  - `forbidigo` banning libovsdb write calls (`Create`, `Update`, `Mutate`, `Delete`, write `Transact`).
  - `depguard` forbidding `pkg/diag` imports from `cmd/ovn-topologyd`, `pkg/graph`, `pkg/server`.
- CI check script `hack/check-deps.sh`: `go list -deps ./cmd/ovn-topologyd` must not contain `pkg/diag` or `k8s.io/client-go/tools/remotecommand`.
- `ui/` scaffold per `AGENTS.md` §7 (dev app `ovn-topology-ui`, package `ovn-topology`), building an empty cluster-level product page.
- `make dev-cluster`: kind + Kube-OVN at a pinned version, reusing Kube-OVN's upstream kind install scripts.
- `hack/capture-fixtures.sh <scenario>`: dumps NB and SB with `ovsdb-client backup` into `testdata/<scenario>/{nb,sb}.db` plus `nodes.json`, `pods.json`.
- CI workflows skeleton: `go.yml`, `ui.yml`, `e2e-pr.yml`, `e2e-nightly.yml`, `release.yml` (dry-run), all path-filtered.

**Out of scope:** any graph logic, any UI beyond the empty page.

**DoD**
- [ ] `make generate lint test build ui-build` passes on a clean checkout.
- [ ] A deliberate libovsdb `Create` call and a deliberate `pkg/diag` import in `pkg/graph` each fail lint (prove it in a throwaway commit or test).
- [ ] `make dev-cluster` brings up Kube-OVN; `capture-fixtures.sh overlay-default` produces fixtures.
- [ ] Empty extension page loads via `make ui-dev` against a Rancher instance.

---

## M1 — Graph core + daemon L3 API (T0)

**Goal:** a correct, joined, incrementally updated graph exposed through a stable API, before any client exists.

**Scope**
- `pkg/ovsdb`:
  - NB + SB monitors with all raft endpoints and `leader_only=false`.
  - TLS from a Secret.
  - Endpoint autodetection (services `ovn-nb`/`ovn-sb`, configurable namespace).
  - Reconnect with backoff, and a full resync on reconnect.
- `pkg/graph`:
  - Node and edge types and joins per `AGENTS.md` §5.
  - Immutable snapshots behind `atomic.Pointer`, with a monotonic `revision`.
  - Incremental updates.
  - Server-side aggregation (LSP counts on LS).
- Views: `l3`, `l2` (focus on an LS, paginated), `chassis`.
- `pkg/server`:
  - `/graph`, `/node/{id}`, `/chassis`, `/version`, `/healthz`, `/readyz`, `/events` (SSE).
  - ETag / `If-None-Match` on `revision`.
- K8s reads: `Node` only in this milestone.
- `charts/ovn-topology`:
  - Daemon chart: distroless, non-root, read-only rootfs.
  - Minimal RBAC per `AGENTS.md` §4.
  - README stating plainly that OVSDB NB has no read-only role.
- Known traps surfaced as data, not hidden:
  - `nat.kind = ovn`, plus an LR-level flag `hasPolicyReroute` for default-VPC `natOutgoing`.
  - A note field explaining that `Iptables*` NAT is not in NB.

**Out of scope:** any client UI, workload identity (Pods/VMs), ACLs, LBs beyond counts.

**DoD**
- [ ] Golden tests pass for every required fixture scenario (§Testing strategy, layer 1).
- [ ] Layer 2 (raw OVN) tests pass on 3-member clustered `ovsdb-server`, including leader kill → reconnect → revision advances.
- [ ] Scale test: 10k LSPs; `/graph?view=l3` p95 < 200 ms, payload < 1 MB.
- [ ] Layer 3 e2e `overlay-default` green in PR CI.
- [ ] `api/openapi.yaml` v1 frozen for M2/M3. Breaking changes after this point require an ADR.

**Follow-up (2026-10-21): live data path audit.** A read of the syncer against libovsdb v0.8.1 found that the snapshot could silently diverge from OVSDB while `/readyz` stayed green.
- **Reconnects:** with reconnect enabled, libovsdb purges and repopulates its cache with no Delete events and no `DisconnectNotify`. Rows deleted during an outage stayed forever, readiness never dropped, and the reconnect rebuild was dead code. The configured inactivity check was never passed to the client either, so half-open connections went undetected.
- **Overflow:** a full event queue dropped events and logged a resync that did not exist.
- **Rebuild:** the rebuild ignored `List` errors.
- **Ignored tables:** routes, policies, gateway/HA chassis, load balancers, encaps, datapaths and BFD were monitored but ignored by incremental updates.
- **Kubernetes:** Nodes and Pods were listed once at start.

The fix:
- **Health probe:** an `Echo` probe drives readiness and triggers a resync (a rebuild from the caches) on reconnect.
- **Overflow:** an overflow triggers a resync that discards stale queued events.
- **Rebuild:** the rebuild publishes nothing on error.
- **Inactivity check:** it is now applied to both clients.
- **Ignored tables:** all monitored tables are applied incrementally.
- **Kubernetes:** Node/Pod informers (list + watch, pods trimmed) bump the revision when identity changes.
- **Tests:** `pkg/ovsdb/syncer_test.go` drives a real libovsdb `TableCache` through `Populate2`/`Purge` (reconnect ghost rows, overflow, rebuild error), mutation-checked. `pkg/graph/kubewatch_test.go` covers late pods and nodes.
- **Still open:** the layer 2 DoD above, against a real clustered `ovsdb-server`.

---

## M2 — TUI: L3 / L2 / Chassis + workload identity (T0)

**Goal:** a usable TUI that proves the graph model end to end. It's cheaper to iterate here than in the extension.

**Scope**
- `cmd/ovn-topo`, Bubble Tea (or a better-suited library if justified in the PR).
  - Left navigation tree (VPC → LR → LS → LSP; Chassis), central detail view.
  - Layer toggle: L2 / L3 / Chassis.
  - NAT, routes and policies visible in the LR detail.
- Data source: either embedded `pkg/graph` against OVSDB via port-forward, or a running daemon's API. Same view code either way.
- Workload identity (extends daemon and API):
  - LSP → Pod → owner (Deployment/StatefulSet/DaemonSet).
  - KubeVirt: `virt-launcher` Pod → VMI → VM, showing the VM rather than the launcher.
  - Multi-NIC: LSP `pod.ns.provider` → `NetworkAttachmentDefinition`. A VM shows all its interfaces across switches.
  - Node join ports (`node-<name>`) → k8s Node.
  - Adds read-only K8s watches on Pod, VMI, VM, NAD (as ADR-0001 allows). Missing CRDs (no KubeVirt) are handled silently.
- **Workload Relationship Display (Pod & KubeVirt VM)**:
  - **Tree View Expansion**: Allow expanding any Logical Switch to list its attached LSPs mapped directly to their Kubernetes workloads:
    - Pods: `[Pod] <name>` (`namespace/<name>`, owner: Deployment/StatefulSet/DaemonSet, IP/MAC).
    - KubeVirt VMs: `[VM] <name>` (resolved from `virt-launcher` pod → VMI → VM, suppressing launcher noise).
    - Multi-NIC VMs: Display all interfaces belonging to the same VM across different subnets.
  - **Port Interconnection Inspector**: When highlighting any port, show the full workload relationship card:
    - Workload Name & Kind (`Pod` or `VirtualMachine`)
    - Kubernetes Namespace
    - Assigned Node & Chassis Hostname
    - Associated Host Veth Interface (`<hash>_h`)
- Machine output: `ovn-topo graph --view l3 -o json|yaml` for scripting (same shape as the API).
- **TUI Interpretation Guide & Screenshot Catalog (`docs/tui-guide.md`)**:
  - User documentation explaining how to navigate and interpret the TUI screens (Topology Tree, Port Interconnection Matrix, and Diagnostics Modal).
  - Recommended screenshot inventory capturing all core OVN / Kube-OVN architectural objects:
    1. **Compound VPC Container**: Logical Router (`LR`) and attached Logical Switches (`LS`) enclosed in a rounded VPC boundary.
    2. **Multi-VPC Interconnect (Transit Switch)**: Shared switch (e.g. `join`) linked to multiple routers for cross-VPC routing.
    3. **Centralized HA Gateway & EIP**: Gateway port highlighted in Persimmon (`#fe7c3f`) showing active chassis node (e.g. `[GW: hv-01]`, `cr-` redirect port).
    4. **Distributed Subnet Gateway**: Internal subnet interface acting as default gateway for workloads (`[GW IP: 10.54.0.1/16]`).
    5. **External Provider Underlay**: Provider network integration via `localnet` port to physical bridge (`br-external`).
    6. **Port Interconnection Inspector**: Detailed inspection box showing MAC, Subnet Prefix, Interface IP, Peer LSP, and Topology Role.
    7. **Tier 1 Diagnostics (`ovn-trace`)**: Logical pipeline simulation with endpoints, visual Hop-by-Hop Flow Path (`[Switch] ➔ [Router] ➔ [Switch] ➔ Port`), and DELIVERED/DROPPED verdict.
    8. **Live `tcpdump` on Dedicated VETH**: Packet capture on isolated workload host veth interface (`<hash>_h`).

**Out of scope:** exec-based anything (M6), ACL/LB views (M4).

**DoD**
- [x] All three views navigable on the `overlay-default`, `custom-vpc-ovn-nat` and `underlay-vlan` scenarios. (Done 2026-10-20, on the `hv-01` lab. These scenarios are not committed as fixtures yet; see tech-debt B1.)
- [x] A KubeVirt VM with 2 NICs on 2 subnets is shown as one VM with two attachments (fixture + e2e). (Done 2026-10-20)
- [x] TUI renders correctly at 80×24 and degrades (truncation, no wrap breakage). (Done 2026-10-20)
- [x] Teatest (or equivalent) golden tests for the main screens. (Done 2026-10-20)
- [x] TUI visual guide (`docs/tui-guide.md`) covering interpretation and screenshot inventory of all OVN object types. (Done 2026-10-20)
- [ ] LR detail lists static routes, policies and NAT rules, not just their counts (scope item "NAT, routes and policies visible in the LR detail"). See the follow-up below.

**Follow-up (2026-10-21): navigation redesign.** After comparing runnable k9s-xray and Miller-column mockups, the four fixed views were replaced by k9s-style xray trees (`:xray vpc|chassis|workload`) with relation jumps, plus the Port Matrix kept as a sortable `:ports` table with an inspector; describe uses `/node/{id}`. Fixes found while doing it: the old layout overflowed 80×24 (header scrolled off, side panel clipped), so the 80×24 item above was not actually met. Every frame is now sized to the terminal, with a test at 60×14, 80×24, 120×40 and 200×60. L2 fetches stopped at the server's default page of 50 ports and now follow all pages. The trace source came from the tree index in other views and now uses the selection. The TUI now consumes `/events` (SSE) with `If-None-Match` polling fallback. Still to do: re-run the three scenarios on a live cluster with the new navigation (verified so far on the fixture and a local fake daemon only).

**Follow-up (2026-10-21): LR routes, policies & NAT (planned, not started).** On a live cluster the LR details pane shows `routes 1 · pol 10 · nat 0` but not the rows.
- **Gap:** the daemon already monitors `Logical_Router_Static_Route`, `Logical_Router_Policy` and `NAT` (`pkg/ovsdb/syncer.go`, `Snapshot.StaticRoutes/Policies/NATs`). Only counts reach clients: the `/graph` LR properties (`pkg/graph/views.go`), and `/node/{id}`, which lists LRPs, switches and bare NAT IDs.
- **API:** add optional arrays to `NodeDetail`, filled for LR only. Regenerate the Go and TS types in the same PR.
  - `staticRoutes[]` (`StaticRoute`): `ipPrefix`, `nexthop`, `outputPort`, `policy` (dst-ip/src-ip), `routeTable`, `bfd`, `ecmp`.
  - `policies[]` (`RouterPolicy`): `priority`, `match`, `action` (allow/drop/reroute), `nexthops[]`.
  - `nats[]` (`NatRule`): `type`, `externalIp`, `logicalIp`, `logicalPort`, `externalMac`, `gatewayPort`.
- **Daemon:**
  - Routes are ordered by longest prefix; `ecmp` is set when a `(routeTable, policy, prefix)` repeats.
  - Policies are ordered by priority, descending. NAT is ordered by type, then external IP.
  - Dangling UUIDs are skipped.
  - The existing NAT related objects get a readable name.
- **TUI:**
  - The LR detail is fetched lazily when the details pane is on a router, and cached per revision.
  - The pane shows *Static routes / Policies / NAT* with about 8 rows each, then `… N more (d describe)`. It shows a loading state, and on error the equivalent `kubectl ko nbctl` command.
  - `d` shows the full lists. A gateway LRP shows the default route that uses it.
- **Next hops:** route nexthops and policy reroute targets are annotated with their owner when the IP is already known to the TUI (LSP addresses, LRP networks). For example, a Kube-OVN node reroute `100.64.0.2` is shown as `node hv-01`. This is an interim client-side lookup; M4's IP resolver replaces it.
- **Modelling choice:** routes, policies and NAT stay rows of the LR detail, not nodes or edges in `/graph`, which keeps `/graph` aggregated. The `NAT`/`Route`/`Policy` node types in AGENTS.md remain addressable through `/node` until a view needs them as graph nodes (M5 path computation). This is an additive T0 field of the existing `/node` contract, so no ADR is needed.
- **Tests:**
  - `pkg/graph`: `GetNodeDetail` ordering and fields, on the fixture's routes and policies.
  - TUI: the pane sections and loading state; the existing fit test covers wrapped matches.
- **Until then:** `kubectl ko nbctl lr-route-list <lr>`, `lr-policy-list <lr>` and `lr-nat-list <lr>`; see `docs/tui-guide.md` §7.

---

## M3 — Rancher extension: L3 view, side panel, live updates (T0) — *parallel with M2*

> **Paused (2026-10-23).** The extension code is removed from the tree and archived at tag `archive/ui-extension` (restore: `git checkout archive/ui-extension -- ui .github/workflows/ui.yml`). Work continues on the daemon and the TUI. Where M4 and M7 depend on M3, only their extension parts wait; their daemon and TUI parts do not. The open DoD items below stay open.

**Goal:** the topology inside Rancher, using the stable M1 API.

**Scope**
- Cluster-level product "Network Topology" (`inStore: 'cluster'`).
- Daemon access via the Rancher cluster service proxy (`AGENTS.md` §7).
- **First task: the SSE-through-proxy probe.** Write the layer 4 e2e that applies a new subnet and asserts the UI updates. Record the result in § Open questions. If SSE is buffered, ship polling with `If-None-Match` as the primary mode.
- L3 view:
  - D3.js (SVG) / Cytoscape.js with compound nodes (VPC) and elkjs layered layout.
  - Incremental updates; positions persisted per view per cluster in `localStorage` (try/catch).
- Side panel from `/node/{id}`, with a raw-columns toggle.
- Empty state when the daemon is missing; API-version mismatch banner.
- Design system per `AGENTS.md` §7: Storybook components first, shell CSS variables only, SVG styles natively inheriting theme variables.
- Release wiring: extension chart (`gh-pages`) and ECI per `AGENTS.md` §8. Validate the `ui/` subdirectory question.

**Out of scope:** L2 and Chassis views in the extension (they come with M4), any T1 feature.

**DoD**
- [x] Layer 4: product visible in Cluster Explorer and Harvester Networks, node count equals `/graph`, side panel, empty state, theme switch. (Done 2026-10-20)
- [ ] Live update: the extension consumes `/events` with the `If-None-Match` polling fallback, and shows the API-version mismatch banner. (Corrected 2026-10-21: the page loads `/graph` once and never refreshes. The earlier tick covered both.)
- [x] Extension installable from the ECI in an offline-mirrored registry (Hauler manifest in release.yml). (Done 2026-10-20)
- [x] No hard-coded colours (ESLint clean, pure SVG with CSS variables). (Done 2026-10-20)

---

## M4 — Relationships (T0)

**Goal:** make the graph *explain* the network. Each feature lands in daemon + API + TUI + extension together.

**Scope** (in this order)
1. **IP resolver.** Every IP in the graph resolves to its owner (LSP, LRP, node join port, external), including route nexthops, policy reroute targets, NAT external/logical IPs, LB backends, and address-set members. Unresolved IPs are marked as external. This is the API `GET /resolve?ip=`, plus inline links in all clients. It replaces the TUI's interim client-side next-hop annotation from the M2 "LR routes, policies & NAT" follow-up.
2. **ACL ↔ Port_Group ↔ Address_Set.**
   - Parse ACL `match` for `@pg` / `$as` references to build edges.
   - "Effective ACLs for this port": ordered by tier, priority and direction, with source objects (NetworkPolicy / SecurityGroup inferred from PG naming and `external_ids`).
   - The match parser must be total. Unparseable matches are shown raw, never dropped.
3. **Load_Balancer ↔ Service.** VIP:port → backends (LSPs), attached LS/LR, health checks. Map to k8s `Service` / `EndpointSlice` (adds read-only watches). Flag dangling backends.
4. **Gateway / HA detail.**
   - Gateway_Chassis and HA_Chassis_Group priorities.
   - Active `cr-` port chassis.
   - SB `BFD` session state.
   - Encap/tunnel mesh in the Chassis view.
5. **KubeVirt live migration.** `requested-chassis` with two entries → in-flight migration (source → destination), activation strategy. Port-security addresses vs actual addresses.
6. **Consistency checks** surfaced as badges and as a `/checks` endpoint:
   - LSP without Pod, Pod without LSP
   - unbound Port_Binding
   - stale chassis (no Node), Node without chassis
   - duplicate MAC/IP
   - tunnel-key collisions

Extension: add the L2 and Chassis views here.

**DoD**
- [ ] Each item has fixtures covering its positive and negative cases, plus golden tests.
- [ ] Layer 3 scenarios extended with expected assertions for each item (`test/e2e/scenarios/*/expected.yaml`).
- [ ] The extension shows all items with Storybook components; layer 4 smoke tests extended.

---

## M5 — Configuration-based path computation (T0)

**Goal:** "how would a packet from A reach B", computed from NB only, with no exec. Available to every consumer.

**Scope**
- `pkg/trace`: src LSP → dst (LSP, IP or Service VIP) + proto/port. It walks:
  - LS → router port
  - LR policies (priority order) and static routes (longest prefix, ECMP aware)
  - NAT, then the egress LRP → LS or external
  - plus the ACLs evaluated at each port (from M4)
- Result: an ordered hop list with the matched object per hop, and a verdict (`delivered`, `dropped-by-acl`, `no-route`, `external`, `unknown`).
- **Every response is labelled `source: configuration` and `approximate: true`.** All UIs show "computed from configuration — not a packet trace".
- API `POST /path` (read-only, idempotent). TUI and extension overlay the path on the L3 view.

**Out of scope:** conntrack state, OpenFlow, anything requiring exec.

**DoD**
- [ ] Path fixtures across scenarios: intra-LS, inter-LS same VPC, VPC → external via SNAT, FIP inbound, default-VPC reroute to gateway node, ACL drop, no route.
- [ ] Where M6 exists, an e2e comparing `pkg/trace` verdicts with `ovn-trace` verdicts on the same inputs. Discrepancies are logged as known limitations, not hidden.

---

## M6 — TUI diagnostics (T1, TUI only)

**Goal:** verify what M4/M5 explain, from the TUI, with the user's own kubeconfig. Governed entirely by ADR-0001.

**Scope**
- `pkg/diag`:
  - `Executor` interface on client-go `remotecommand`.
  - Typed command allowlist; arguments built from graph objects only.
  - Pod discovery for `ovn-central`, `ovs-ovn`, `kube-ovn-cni` per node.
- `--diag` mode with a persistent DIAG badge. RBAC denial degrades to T0 with a message.
- Features (in this order):
  1. **Point-and-click `ovn-trace`.**
     - The user picks the source/destination in the TUI, and the TUI builds the full microflow match: inport, eth.src/dst (next-hop router MAC), ip.src/dst, proto/port.
     - Supports real-time fzf-style fuzzy search, category filtering (`[VM]`, `[Pod]`, `[Gateway]`, `[Node]`, `[Public DNS]`), and direct Custom Target IP entry (`Search / Target IP: <ip>[:port]`).
     - Supported protocols: ICMP (Ping), TCP 22 (SSH), TCP 80 (HTTP), TCP 443 (HTTPS), UDP 53 (DNS).
     - It runs in `ovn-central` leader (`ovn-sb-leader=true`) using 4x `--ct=new` conntrack evaluation.
     - Output is parsed into stages, overlaid with visual Hop-by-Hop flow paths, and evaluated with clear verdicts (`DELIVERED`, `DROPPED`, `NO-ROUTE`). Raw output toggling (`o` key) is always available.
  2. **tcpdump.**
     - *Port mode:* LSP → chassis → node → OVS Interface (`external_ids:iface-id`) → `<hash>_h` veth. Runs line-buffered (`-l`) tcpdump in that node's `kube-ovn-cni` container.
     - Fallbacks safely to UP physical interfaces (`br-external`, `ovn0`) rather than down `br-int`.
     - Filter presets available: ICMP, TCP, filtered Port IP.
  3. **Physical wiring.** `ovs-vsctl show` executed inside the node's OVS pod. Displays bridges (`br-int`, `br-external`), ports, interfaces, and VXLAN/Geneve tunnel endpoints.
  4. **Filtered Conntrack.** Active flows for port IP dumped via `ovs-appctl dpctl/dump-conntrack` on the hosting node and filtered in real-time.
  5. **Log tails.** `ovn-controller` logs tailed on the port's node with dynamic node hostname resolution.
  6. **k9s-inspired UI layout.** Clean top bar for view tabs and diag status, spacious explorer in the center, and a 2-row docked bottom footer with real-time connection status and `<key>` action badges.

- **Relationship with `kubectl ko` (`pkg/ko`)**:
  Rather than importing `github.com/kubeovn/kube-ovn` as a Go module (which would violate `AGENTS.md` by pulling in GoBGP, Calico/Felix, and querying CRDs), `pkg/diag` adopts the proven command patterns from `pkg/ko` directly:
  - **`ovn-trace` formula**: Matches `pkg/ko/trace.go` using `ip.ttl == 255`, protocol matchers, and 4x `--ct=new` for multi-tier NAT/ACL conntrack simulation.
  - **Physical OpenFlow trace**: *Not implemented* (correction 2026-10-23: `pkg/diag` has no `ofproto/trace` builder). Planned in the follow-up below.
  - **Host interface resolution**: Resolves dedicated host veth interfaces via `ovs-vsctl --bare --columns=name find interface external_ids:iface-id=<lsp>`.
  - **Raft leader resolution**: Selects `ovn-sb-leader=true` and `ovn-nb-leader=true` pods dynamically.
  - **Workload-Aware Diagnostic Context**: The diagnostics modal (`x` key) automatically reflects the resolved Pod / VM relationship of the selected port (`Target Workload: [VM] vm-onewater on hv-03 via 4e3b7_37a8eec_h`). `tcpdump` targets the workload's dedicated host veth, and `ovn-trace` simulates microflows directly from the workload's ingress port.
  - **Dual-Pane L2 Subnets Explorer**: Allows navigating subnets on the left pane and focusing individual workload cards on the right pane with `Tab`/`←`/`→`/`Enter`, targeting diagnostics directly on any selected VM or Pod.

**Out of scope:** any T1 feature in the daemon or extension; raw command passthrough; port mirroring.

**DoD**
- [x] `hack/check-deps.sh` still proves the daemon is free of `pkg/diag`.
- [x] Point-and-click `ovn-trace`, live `tcpdump`, conntrack, `ovs-vsctl show`, and controller log tailing implemented in TUI.
- [x] Layer 3 e2e for trace and tcpdump command builders verified against real Kube-OVN/OVS outputs on Harvester `hv-01`. (Manual lab run; no automated layer 3 suite exists yet, see tech-debt B2.)
- [x] Automated regression tests prevent view and pane index mismatches (now `TestDiagTargetIsAlwaysTheSelection` and the executor-level tests, see the follow-up below).
- [x] Every `pkg/diag` command has a unit test asserting the exact argv produced from a fixture object (no injection possible from names containing spaces or quotes).

**Follow-up (2026-10-21): diagnostic target audit.** The git history had a series of "the diagnostic acted on something other than the selection" fixes. An audit of the selection → command path found the root cause: the target was never captured. Every action and every modal redraw re-derived it from the cursor, through helpers with silent fallbacks.
- **Wrong object:**
  - A switch took its first pod; a router took its first port.
  - A chassis traced from the cluster's first router port; a multi-NIC VM took its first NIC.
  - A switch shared by several routers took the first router's MAC.
  - The trace defaulted to `ovn-default` / `10.244.0.1`.
- **Wrong host:**
  - A port's node came from Kubernetes metadata, falling back to the gateway node.
  - With no node, actions ran on the first cluster node or on the literal `cluster-node`.
- **Wrong interface:** `FindOVSInterface` never failed. It fell back to `ovn0`/`br-external` (by name substring) or `br-int`, while the header still named the port.
- **Drift:** a live update that moved the row put the cursor at the top, and the next action ran on that row.

The fix:
- **Captured target:** `cmd/ovn-topo/diagtarget.go` resolves one immutable target when `x` is pressed. It records the port, datapath, IP/MAC, and the node with its source (Port_Binding chassis, active gateway chassis or selected chassis). Every action reads only that target.
- **No borrowing:** anything not derivable from the selected object disables the action, with the reason shown in the menu. Switches, routers and VPCs ask for a port. A multi-NIC workload asks which NIC. An ambiguous next hop is an error.
- **Live updates:** the target is revalidated by ID. It goes stale if the object disappears, and you are told if it moves (e.g. live migration).
- **`pkg/diag`:** the interface lookup runs only on the target's node, with a typed `BuildFindIfaceByIfaceIDCommand`, and returns `ErrIfaceNotFound` instead of guessing. Pod lookups re-check `spec.nodeName`. The tcpdump interface lookup moved off the UI goroutine.
- **Tests:**
  - Every row of every xray must resolve to itself.
  - Each action is checked through `MockExecutor` for the pod, node and arguments it uses.
  - No-guess cases are covered.
  - Drift and the multi-NIC picker are covered.
  - Mutation-checked: re-adding "switch → first pod" or "missing iface → ovn0" fails the suite.

**Planned follow-up (2026-10-23): OpenFlow trace + `ovn-detrace`.** Not started; needs Q11 answered first.

`ovn-trace` simulates the logical pipeline from NB/SB: it says what the *configuration* does, not whether `ovn-controller` installed those flows on the node. Add two steps to the trace action:
- `ovs-appctl ofproto/trace br-int` in the source node's `ovs-ovn`, with the same microflow as the `ovn-trace` match (`in_port=<host veth>` from the captured target, `dl_src/dst`, `nw_src/dst`, `nw_ttl`, ports). Typed builder, no shell.
- `ovn-detrace`, fed the `ofproto/trace` output on stdin (`Executor.Stream`). It maps each OpenFlow `cookie` to the SB `Logical_Flow` and the NB object behind it (ACL, LB, route, NAT), so a drop names its ACL.

Each tool gets its own verdict, derived from the expected chassis/port in the graph. A logical-vs-physical disagreement (stale `ovn-controller`, missing flows) is shown as a banner, not averaged. If `ovn-detrace` is absent from the image it is disabled with the reason; nothing is installed at runtime (air-gap, read-only).

*DoD (when scheduled)*
- [ ] Q11 answered on `hv-01` with evidence.
- [ ] `BuildOfprotoTraceCommand` and `BuildDetraceCommand` are typed, with exact-argv tests including hostile names.
- [ ] The trace result shows logical (`ovn-trace`), physical (`ofproto/trace`) and annotated (`ovn-detrace`) outputs, one verdict per tool, and the disagreement banner.
- [ ] Parser tests on outputs captured from `hv-01` (see E3).
- [ ] Missing `ovn-detrace` disables only that step, with the reason.
- [ ] `hack/check-deps.sh` still clean; nothing added to the daemon.

---

## M7 — Harvester validation + release hardening

**Goal:** prove the "Harvester + RKE2" claim and make releases boring.

**Scope**
- Layer 5:
  - Self-hosted runner against the lab Harvester (dedicated namespace/VPC, created and destroyed per run).
  - Then ephemeral nested Harvester (single node, Kube-OVN addon) nightly.
- Validate endpoint autodetection, TLS and namespaces for Harvester's Kube-OVN addon. Record the findings in § Open questions and the chart README.
- Same `expected.yaml` scenario assertions reused on Harvester (VM-centric scenarios: VM with 2 NICs, VM live migration, VPC + FIP to a VM).
- Version matrix on release: Kube-OVN current and current-1; Rancher latest; Harvester current (and previous, on RC only).
- `release.yml`:
  - One version bump → `vX.Y.Z`, `ovn-topology-X.Y.Z`, `ovn-topology-ui-X.Y.Z` tags.
  - Images, charts, ECI, Hauler manifest, SBOM, signed images (cosign).

**DoD**
- [ ] Nightly Harvester run green for 5 consecutive nights.
- [ ] A release produced end to end from CI only; installed offline from the Hauler bundle in the lab.

---

## M8 — Topology history & diff (T0, ADR-0002)

**Goal:** answer "what changed since it last worked?" and show the topology at any point in time since the daemon started.

**Scope**
- `pkg/history` per ADR-0002:
  - **Recorder:** OVSDB row-level changes for consumed tables, stamped with time / epoch / revision / schema version.
  - **Checkpoints** every N changes or T minutes, and on every (re)connect.
  - **Noise control:** coalescing plus a denylist decided from measured churn.
  - **Gap and coalesced records.**
  - **Store interface** with a `memory` implementation (default, bounded) and a `bbolt` implementation (opt-in, PVC).
  - **Reconstructor:** checkpoint + replay through the current `pkg/graph`.
  - **Summarizer:** row changes → human-readable events. Initial event catalogue:
    - LS/LR/LSP/LRP added/removed
    - port bound/unbound/moved chassis
    - NAT added/removed/retargeted (FIP moved)
    - route/policy changed
    - gateway failover (active `cr-` chassis changed)
    - chassis up/down
    - ACL/PG membership changed
    - LB backend set changed
    - VM migration started/finished

    Anything unmapped surfaces as a generic row event.
- The recorder may be built in parallel after M1. The summarizer events that need M4 relationships land once M4 is done.
- API (add to `api/openapi.yaml`, still read-only and idempotent):
  - `GET /history/events?since&until&kind&object&limit` — change feed, paginated, newest first.
  - `GET /graph?...&at=<RFC3339|epoch:revision>` and `GET /node/{id}?at=` — reconstructed state.
  - `GET /diff?from&to&view` — added / removed / changed nodes and edges, with attribute-level diffs.
  - `GET /history/coverage` — retention window, gaps, mode, store size.
- Chart: `history.mode` (`memory`|`persistent`), retention limits, optional `persistence.storageClass`/`size`. The default install stays stateless.
- TUI:
  - A change-feed pane, filterable by object or kind, with jump-to-object.
  - `--at <time>` to browse the past, with a visible "HISTORICAL" badge.
  - `ovn-topo diff --from --to [-o json]`.
- Extension:
  - A timeline panel (change feed with gaps drawn as breaks).
  - Time scrubber on the L3 view.
  - Diff mode: added, removed and changed elements styled with shell tokens, and a legend.
  - Kubernetes enrichment shown with a "current, may differ from then" label in historical mode.

**Out of scope:** S3 archival/export (backlog), multi-replica history, history of Kubernetes objects, alerting on changes.

**DoD**
- [ ] Reconstruction correctness: for every layer 3 scenario, apply a scripted sequence of changes. Then `graph(at=Tk)` must equal the live graph captured at Tk, for every step k (golden comparison, UUID-agnostic).
- [ ] Gap handling: kill the daemon mid-sequence (memory and persistent modes) → a gap is recorded, a coalesced change set is correct, and no interpolation happens across the gap.
- [ ] Schema-upgrade case: retention spans a Kube-OVN/OVN upgrade (two pinned versions in CI) and old checkpoints still reconstruct.
- [ ] Summarizer: each catalogue event has a fixture producing it, and `ha-gateway-failover` and `kubevirt-multinic` (with migration) emit the expected events in order.
- [ ] Performance on the 10k-LSP scale fixture with 24 h of synthetic churn:
  - `graph(at)` p95 < 500 ms
  - `/diff` over 24 h p95 < 1 s
  - memory mode stays within its configured byte limit
- [ ] Layer 4 e2e: the timeline shows a subnet creation done during the test, and diff mode highlights it.

---

## M9 — MCP server (T0 tools only)

**Goal:** let AI agents answer network questions ("why can't vm-a reach vm-b?", "what changed in tenant-1 overnight?") from real data, kvtop-style.

**Decisions (binding for this milestone)**
- **Transport and host:** `ovn-topo mcp` subcommand over **stdio**, using the user's kubeconfig.
  - **Backend A:** a daemon reached via the Kubernetes service proxy. Full feature set, including history.
  - **Backend B:** embedded `pkg/graph` against OVSDB through a port-forward. No history tools.

  Streamable HTTP inside the daemon is backlog, not this milestone.
- **T0 only, always.** No `pkg/diag` tool is exposed through MCP, even when `--diag` is set. An agent must not be able to start a tcpdump or exec into nodes. This is enforced by `depguard` (`pkg/mcp` must not import `pkg/diag`).
- Every tool is annotated read-only (`readOnlyHint: true`, `destructiveHint: false`, `idempotentHint: true`).
- SDK: the official Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`), unless kvtop already uses a different one. In that case align with kvtop and note it in the PR.

**Scope**
- `pkg/mcp`: tool definitions and handlers over a `Backend` interface (daemon client or embedded graph).
- Tools:

  | Tool | Purpose |
  |---|---|
  | `find` | search objects by name, IP, MAC, pod/VM, namespace; returns IDs + one-line summaries |
  | `get_graph` | view (`l3`/`l2`/`chassis`), `focus`, `depth`, optional `at` |
  | `get_node` | full detail of one object, optional `at` |
  | `resolve_ip` | IP → owner (LSP/LRP/node/external) |
  | `effective_acls` | ACLs applying to a port, ordered, with source objects |
  | `compute_path` | M5 path; result always carries `approximate: true` and `source: configuration` |
  | `run_checks` | consistency/health checks, filterable by scope |
  | `list_changes` | M8 change feed (`since`, `until`, `object`, `kind`) — backend A only |
  | `diff` | M8 diff between two times — backend A only |
  | `history_coverage` | retention window and gaps — backend A only |

- **Output discipline for LLM consumers:**
  - Compact JSON with a stable shape and a one-line `summary` field.
  - Hard caps on items per response, with a `next` cursor.
  - Every response carries `revision`, `timestamp`, and `historical: true|false`.
  - Tools that would exceed caps return aggregated counts plus a hint to narrow `focus`.
- **Prompt-injection hygiene:**
  - Object names, `external_ids` and descriptions are cluster-user-controlled data. Return them only inside structured fields, never interpolated into prose summaries.
  - The `summary` field is generated from typed fields only.
- MCP `resources`: none in this milestone (tools only). Optional `prompts`: `troubleshoot-connectivity` and `what-changed`, templates that chain the tools above.
- Docs: a `docs/mcp.md` section with client configuration examples (Claude Code, OpenCode, Gemini CLI) and the T0-only guarantee.

**Out of scope:** exec/diagnostic tools, write tools, HTTP transport in the daemon, auth beyond the user's kubeconfig.

**DoD**
- [ ] `depguard` proves `pkg/mcp` has no path to `pkg/diag`.
- [ ] Golden tests of every tool's output on fixtures (shape, caps, cursors, `summary` built only from typed fields).
- [ ] Injection test: a fixture with LSP names / `external_ids` containing instruction-like text. The text never appears outside its data field.
- [ ] Scenario eval (`test/mcp/`): a set of questions per layer 3 scenario, each with expected facts (e.g. "path vm-a → vm-b verdict = dropped-by-acl, ACL = …"). These are run as deterministic tool-call scripts in CI (no LLM in CI). An optional manual run with a real agent is recorded in the PR.
- [ ] Works with backend A (daemon via service proxy) and backend B (embedded); history tools return a clear "not available with this backend" error on B.

---

## Backlog (not scheduled — do not start without an explicit task)

- **ACL hit counters (T1):** OpenFlow cookie → logical flow → ACL, aggregated across nodes (`ovn-detrace`-style). Builds on the M6 `ovn-detrace` follow-up.
- **OVN-IC:** two-cluster interconnect view (`icnbctl`/`icsbctl` databases as an additional monitor).
- **Prometheus overlay (T0):** chassis up, raft health, `subnet_used_ip_count`, pinger loss/latency, OVS interface rx/tx on LSP edges. Optional; requires Rancher Monitoring.
- **History archival/export:** periodic checkpoint + change upload to S3-compatible storage for long retention (ADR-0002 follow-up).
- **MCP over streamable HTTP in the daemon:** needs an auth story beyond kubeconfig; own ADR.
- **Segmentation audit report:** reachability matrix (namespace/VPC) from ACL/PG/AS + path engine, exported as signed evidence. Configuration-based, labelled as such.
- **Fleet view:** cross-cluster inventory and CIDR-overlap detection in a global Rancher product page.
- **Identity adapter boundary:** isolate Kube-OVN conventions (`external_ids`, `cr-`, `join`) behind an interface to allow ovn-kubernetes / OpenStack later. No other adapter without a request.
- **SB Logical_Flow browser:** per datapath, on demand, expert mode.
- **Extension diagnostics component:** would need its own ADR (see ADR-0001 Consequences).

---

## Testing strategy (applies to all milestones)

| Layer | Runs on | Purpose | When |
|---|---|---|---|
| 1. Unit + golden | libovsdb in-process `server` loaded with `testdata/` snapshots | joins, views, aggregation, API shape | every PR |
| 2. Raw OVN | 3-member clustered `ovsdb-server` NB/SB containers; `ovn-nbctl`/`ovn-sbctl` topology scripts; optional `ovn-fake-multinode` | schema drift across OVN versions, raft follower reads, reconnect, TLS, scale | every PR |
| 3. Kube-OVN e2e | kind + Kube-OVN (upstream scripts, pinned) | real Kube-OVN output; daemon chart, RBAC, autodetection; M6 diagnostics | PR: `overlay-default`; nightly: all scenarios |
| 4. Rancher + UI e2e | Rancher on the kind cluster + extension (ECI or UIPlugin); Playwright | service proxy, SSE, product registration, rendering | nightly + release |
| 5. Harvester | self-hosted runner (lab), then ephemeral nested Harvester | Harvester addon layout, VM scenarios | nightly / pre-release |

**Scenarios** (`test/e2e/scenarios/<name>/` = manifests + `expected.yaml` with structural assertions, never UUIDs):
`overlay-default`, `distributed-gw`, `custom-vpc-ovn-nat`, `vpc-nat-gw` (asserts the "not OVN NAT" explanation), `underlay-vlan`, `dual-stack`, `kubevirt-multinic`, `ha-gateway-failover`; later `ovn-ic`.

**Fixture loop:** every layer 3 scenario exports NB/SB snapshots as CI artifacts. When Renovate bumps Kube-OVN, a bot PR refreshes `testdata/` so layer 1 always runs against real Kube-OVN output.

**Layout**
```
test/
├── e2e/                  # Go, build tag e2e (layers 3 and 5)
│   └── scenarios/<name>/ # manifests + expected.yaml
├── ovn/                  # layer 2: compose + topology scripts
├── ui/                   # Playwright (layer 4)
hack/kind/  hack/harvester/
```

---

## Open questions

Record answers here with evidence (date, link to test run or doc).

| # | Question | Blocks | Status / answer |
|---|---|---|---|
| Q1 | Does the Rancher service proxy pass SSE without buffering? | M3 live updates | **Resolved (2026-10-20)**: Reverse proxies may buffer chunked HTTP. Clients must degrade: attempt SSE on `/events` and fall back to polling `/graph` with `If-None-Match`. The TUI does this. **The extension does not consume either yet** (2026-10-21, see the M3 DoD). Whether the Rancher proxy actually buffers has not been measured. |
| Q2 | Do the Rancher reusable extension workflows run from `ui/`? | M3 release | **Resolved (2026-10-20)**: No. Reusable workflows hardcode `working-directory: ./`. Solved via custom workflow steps in `.github/workflows/ui.yml` and `release.yml`. |
| Q3 | Harvester Kube-OVN addon: namespace, service names, TLS secret, OVN version | M1 autodetect defaults, M7 | **Resolved (2026-10-20, lab hv-01)**: Namespace is `kube-system`, services are `ovn-nb:6641` and `ovn-sb:6642`. TLS is disabled (`ENABLE_SSL: false`). Pods use `hostNetwork: true` and bind to node IP. |
| Q4 | Which OVN version to pin in `hack/ovn-version` (match Harvester's addon and upstream Kube-OVN stable)? | M0 codegen | **Resolved (2026-10-20, lab hv-01)**: OVN `24.03`. Pinned to `v24.03.2` in `hack/ovn-version`. |
| Q5 | KubeVirt LSP naming for secondary NICs across Kube-OVN versions | M2 identity | **Resolved (2026-10-20, lab hv-01)**: KubeVirt VMs attached to Kube-OVN NADs follow the pattern `<vm-name>.<namespace>.<nad-name>.<vlan|subnet>.ovn`. Default management interfaces (`multus: default/default`) bind to the physical bridge `mgmt-br` outside OVN and do not create OVN LSPs. |
| Q6 | Does standalone (non-Rancher) Harvester UI need support? | scope | **No** for now: Rancher-managed Harvester only |
| Q7 | Should `pkg/diag` import `github.com/kubeovn/kube-ovn/pkg/ko` directly? | M6 | **Resolved (2026-10-20)**: **No.** Importing `github.com/kubeovn/kube-ovn` pulls 60+ heavy dependencies (`gobgp`, `felix`, `cni`) into `go.mod` and violates `AGENTS.md` rule 2 by querying Kube-OVN CRDs (`Subnet`, `Vpc`). Instead, `pkg/diag` adopts `pkg/ko`'s exact command generation logic while using our in-memory OVSDB graph as the sole source of truth. |
| Q8 | Measured monitor churn per table (to set the ADR-0002 noise denylist and checkpoint cadence) | M8 | open; measure on `overlay-default` + `kubevirt-multinic` |
| Q9 | Which MCP Go SDK does kvtop use? Align or justify | M9 | open |
| Q10 | How does OVN handle inter-VPC traffic in Kube-OVN without VPC peering? | M6 trace | **Resolved (2026-10-20, lab hv-01)**: In Kube-OVN, separate VPCs (separate Logical Routers) route non-local traffic to `0.0.0.0/0` via the VPC's external gateway (`cr-vpc-<name>-external`), egressing through `localnet.external` onto the physical underlay. `ovn-trace` verifies packet delivery to `localnet.external`; the physical upstream firewall then enforces inter-subnet access. |
| Q11 | Is `ovn-detrace` (and its python `ovs` bindings) shipped in the Kube-OVN images on Harvester, and in which pod can it reach NB, SB and the node's OVS db? Candidates: `ovs-ovn` (local OVS db; NB/SB over the `ovn-nb`/`ovn-sb` services, TLS?) or `ovn-central` (NB/SB sockets; `--ovsdb` only names ports?). | M6 detrace follow-up | open; check `which ovn-detrace` and a dry run in both pods on `hv-01` |
