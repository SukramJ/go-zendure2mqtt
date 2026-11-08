# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A pure-Go daemon that bridges **Zendure** devices (e.g. SolarFlow 2400 AC) to **MQTT**
with **Home Assistant** auto-discovery — locally over the device's on-board HTTP API
(zenSDK) or via the Zendure cloud (TLS MQTT). Twin of `go-daikin2mqtt` / `go-mtec2mqtt`;
shares their setup (custom MQTT client, declarative catalog, distroless image, HA add-on).
Two binaries: `zendure2mqtt` (daemon) and `zendure2mqtt-util` (diagnostics CLI).

`docs/konzept.md` is the design doc and milestone plan. **State: M0–M4 complete** and
verified against a real SolarFlow 2400 AC — local + cloud transports, HA discovery with
battery sub-devices and i18n, virtual switches, a diagnostic web UI, an mDNS browser, and
a Home Assistant add-on.

## Commands

```bash
make build            # both binaries → bin/ (ldflags inject version/commit/date)
make test             # full suite with -race
make check            # pre-commit gate: vet + fmt-check + lint + test
make fmt              # gofumpt + goimports
go build ./... && go test ./...   # quick local loop (no dev tooling needed)

# util CLI
go run ./cmd/zendure2mqtt-util report   --host 192.168.1.50
go run ./cmd/zendure2mqtt-util resolve  --host 192.168.1.50 --lang de
go run ./cmd/zendure2mqtt-util cloud-login --token <app-token>
go run ./cmd/zendure2mqtt-util discover
```

Go ≥ 1.27, `CGO_ENABLED=0`. Minimal deps (`golang.org/x/sync`, `yaml.v3`) — keep it that way.

## Which model does which work

Set by the owner for the go-fabric family on 2026-10-10 (go-fabric's
`CLAUDE.md`); the review split of 2026-11-08 (go-fabric #456) likewise.
Adopted here on 2026-11-08. A statement about the zenSDK local API, the Zendure cloud or Home Assistant discovery that
lands in this repo is cited later as fact — a comment that names a caller
or a consumer, a docstring that states what the other side does, a
`changelog.md` line — so the work that produces such statements gets
the strongest model, and the work a guard checks may use a cheaper one. The
agents in [Sub-Agent Delegation](#sub-agent-delegation) carry a default
model each; the rows below decide when a call overrides it (`model:` on the
Agent tool). Implementation defaults to Opus.

| Work | Model |
| --- | --- |
| Analysis, planning, reviews of design, audits; anything written into `changelog.md` or a comment that names a consumer, or a comment that names a zenSDK or Zendure cloud behaviour, or a Home Assistant discovery behaviour; the cloud login and token decoding (`internal/zendure/cloud/`), the TLS config in `outputBrokerConfig`, `config.yaml` with its credentials; *which* guard gets built and its bite line; rebases and merge conflicts where `main` may have overtaken the change | **Fable** (the default for this repo; also the `hunt` agent) |
| Implementation of a finished plan with guards behind it: a device property once the pattern exists, a discovery field, a test on a recorded response, a `changelog.md` entry for a finished diff | **Opus** (the `impl` default; also what fast mode runs on) |
| **Review of a slice's pull request, split (owner decision 2026-11-08, go-fabric #456).** The code review — correctness, concurrency, error paths, tests, the fixed report shape — is **Opus**. On top of it **Fable** reads, bounded: every new comment or docstring that names a zenSDK or Zendure cloud behaviour, or a Home Assistant discovery behaviour against a response recorded from the SolarFlow 2400 AC, go-hamqtt at its tag, every line that enters `changelog.md` or a comment that names a consumer, the paths of the first row, and the brief's decisions against the reference — the behavioural gaps a code review does not see. The load-bearing claims of the Opus review are verified before they reach the implementer. | **Opus**, then **Fable** |
| Work a test or generator checks immediately: `make fmt`, `make tidy-check`, a table case; search and first reading of a tree whose result a stronger model or the owner reads again | **Sonnet** (the `guard` default; `impl` with `model: "sonnet"`) |
| Mechanical inventories whose answer is a list the caller can re-grep: which files reference X, which links are dead, which entries lack a counterpart | **Haiku** (`sweep` default) |

Sonnet never writes a claim about a zenSDK or Zendure cloud behaviour, or a Home Assistant discovery behaviour into the repo, and
never touches the paths named in the first row. Haiku returns lists, never
verdicts. When in doubt about which row a task is in, it is in the first.

---

## Sub-Agent Delegation

As go-fabric's and openccu-loom's rule (owner, 2026-10-10). The main
conversation owns planning, contract and wire decisions, guard
specifications, sub-agent steering, and final verification. It does not own
typing.

**Delegate what a gate can prove; keep what only a reader can accept.**
Delegate freely: table cases, fakes, scaffolding, fixture updates, doc and
link cleanups, version bumps across the changelog set, inventories and grep
sweeps. Never delegate: `internal/zendure/cloud/` login and signing, the TLS config, the topic layout, and *which* guard gets
built. Locating is delegable; reading is not.

| Agent | Default model | Use for |
|---|---|---|
| `impl` | Opus | scoped implementation with a stated acceptance command |
| `guard` | Sonnet | tests from a caller-written guard spec, plus the bite proof |
| `sweep` | Haiku | read-only inventories and grep sweeps, high fan-out |
| `hunt` | Fable | adversarial read-only defect hunt, ranked candidates |

Each default is one row of
[Which model does which work](#which-model-does-which-work); a call that
falls into another row passes `model:` on the Agent tool — `impl` with
`model: "sonnet"` for work a generator or formatter checks immediately.

**Size CPU-bound fan-out from the host, never from a constant** — the owner
works from a 4-core box and a 14-core box:

```sh
cores=$(nproc); agents=$(( (cores - 1) / share ))   # share = cores per agent
```

Check the 1-minute load first; if it is at or above the core count, run one
CPU-bound agent and queue the rest. Eight concurrent agents is a practical
ceiling whatever the core count. Every CPU-bound agent must **pin** its share
in the command it runs (`GOMAXPROCS=<share> go test -p <share> ./internal/x/...`, `golangci-lint run --concurrency=<share>`) — handing out a number without
pinning it changes nothing. Read-only agents cost no core worth counting;
6–8 is a practical ceiling there.

Two hard rules: **no sub-agent runs the full gate (`make check` (vet, fmt-check, lint, tidy-check, race tests), `make fuzz-smoke`) or a
repo-wide lint** — the full gate runs once, in the main conversation, at the
end of the slice — and **no two writing agents in the same package**
(disjoint file sets, or `isolation: "worktree"`).

Every brief carries five things: the files and public surface plus one style
reference; the acceptance command the agent runs itself; the invariants it
touches; **stop conditions** (new dependency, API or wire change, credential
handling, an ADR-shaped decision — a stop is a successful outcome); and a
report format ≤ 250 words, large artefacts to the scratchpad as a path.

**A report is a claim.** Read the diff, run the gate yourself. For `guard`,
the acceptance artefact is not a green test but the **bite proof**: the named
production line removed, the test observed red with its message, the line
restored, green again.

---

## Token use

Set by the owner on 2026-10-14 ("Wichtig ist mir eine effiziente Nutzung
der Token"), as go-fabric's rule of the same date. A slice costs on
the order of half a million to a million tokens per implementing agent
before the owner's review; the rules below are what keeps that from
doubling.

- **Read slices, not files.** `sed -n a,bp`, `grep -n` with a tight
  pattern and `head`; a whole file only when the task is the file. Never
  re-read what is already in context; cite the earlier read.
- **Delegate first reads.** A source tree's inventory (go-hamqtt, go-mqtt, this repo)
  is a `sweep` or Sonnet read written to the scratchpad with `file:line`
  for every fact; the owner spot-checks the load-bearing lines and verifies
  every fact about the zenSDK local API, the Zendure cloud or Home Assistant discovery before it enters a brief or
  `changelog.md` or a comment that names a consumer — the agent's result is a map, never a citation.
- **Logs by their summary.** The gate (`make check` (vet, fmt-check, lint, tidy-check, race tests), `make fuzz-smoke`) runs in the
  background with a summary file (one line per gate, `FAIL`/`ok` lines
  only); read the summary, and open a log only on failure, with `grep` for
  the failure markers and the error strings — never `cat`. The same for CI:
  the job log is fetched once to a file and grepped.
- **One turn, every independent call.** Reads, checks and background
  starts that do not depend on each other go out together; a wait is a
  `Monitor` or an `until` loop in the background, not polling by hand.
- **Short artefacts.** A brief names files, decisions and the acceptance
  command, not prose; a report follows the fixed shape of
  [Sub-Agent Delegation](#sub-agent-delegation) (≤ 250 words, large
  artefacts as a scratchpad path); status messages to the owner are short,
  outcome first, in German.
- **Skip what cannot have changed.** No regeneration for a comment change;
  no second gate run for a notes-only commit — say what was skipped and
  why.
- **Edit by script.** Several replacements in several files are one
  scripted call with asserted anchors, not a chain of interactive edits;
  a generated or formatted artefact is regenerated, never patched.
- **Agents read what the brief names.** An implementing agent gets the
  file list and the pinned sources in the brief, reads those, and reports
  in the fixed shape; it does not survey the tree. One agent per brief;
  two agents only on disjoint paths, at most one Opus agent at a time.
- **Push, pull request and tag are the session's own calls.** An
  implementing agent works locally — branch, commits, guards — and
  reports; the owner's session reads the diff, runs the gate, pushes,
  opens the pull request and tags the merge commit; the owner merges. A
  delegated push, pull request or tag is refused by the auto-mode
  classifier ("Create Public Surface", 2026-11-07), and the review in
  [Which model does which work](#which-model-does-which-work) sits
  between the agent's report and the push anyway.

---

## Architecture

**Core idea:** local and cloud share one property model; they differ only in transport.
A transport-neutral `source.Backend` (`Source` with `Run`/`Read` + `Controller` with
`Write`) has two impls: `local` (HTTP poll/write) and `cloud` (TLS MQTT pub/sub). Everything
downstream — catalog, process, coordinator, hass, state/web — is transport-neutral.

### Runtime wiring (`cmd/zendure2mqtt/main.go`, `run()`)
Load config → load catalog (`zendure.yaml`) → `buildBackend` (local or cloud per
`CONNECTION`) → connect the output MQTT (custom client + Lifecycle, LWT
`<name>/connected=0`) → optional `hass.Discovery` and `state.Store` → coordinator +
`publisher.Instance` (`<name>/info`, maintenance routes on the coordinator's command
router) → `coordinator.Run` (+ optional `web` server, stats loop) in an errgroup; on
graceful shutdown (signal or maintenance restart, which cancels the same context)
publish `<name>/connected=0`, then stop with exit 0.

### Coordinator (`internal/coordinator/`)
Subscribes `<root>/+/+/+/set`, then drives the backend via an `onReading` callback →
`publish()`: `process.Resolve(report, catalog, lang)` + synthetic `switchPoints` → update
the optional `state.Store` → HA discovery (once per unique_id) → publish each point's state.
Inbound `…/set`: `handleSwitchSet` (virtual switches) first, else `catalog.ByTopic` →
`decodeCommand` (select label → code in either language; numeric value un-scaled to raw) →
`backend.Write`. Every successful write triggers `reReadSoon` (≈750 ms later: one-shot
`Read` + republish) so HA reflects the change sub-second.

### Backends (`internal/zendure/`)
- `model` — `Report`/`WriteRequest`, the shared data shape (`ParseReport`).
- `local` — `FetchReport` (`GET /properties/report`), `WriteProperties`
  (`POST /properties/write`), a one-shot `Read`, and a poll `Backend` (a goroutine per device).
- `cloud` — `DecodeToken` (base64 → `<api_url>.<appKey>`) + signed `Login`
  (`POST /api/ha/deviceList`, `sign = SHA1(HAKEY + sorted(params) + HAKEY)`), then a **TLS
  MQTT stream**: subscribe `iot/{productKey}/{deviceId}/properties/report`, publish control
  to `.../properties/write`. Its own **event-driven, stability-aware reconnect loop**
  (`connectLoop` on `TCPClient.ConnectionLost()`): occasional drops recover in ~1 s, a
  flapping broker is throttled 1→2→…→30 s. `CLOUD_TLS_VERIFY` (default false) toggles strict
  cert verification — the Zendure cloud cert is non-standard. **Caveat:** the cloud drops
  this clientId every ~1 s (single-session), so cloud mode trickles telemetry; prefer local.

### Catalog & process (`zendure.yaml`, `internal/catalog`, `internal/process`)
`Entry` maps a raw property → topic/group/platform/unit, with `offset`/`scale`
(`value = (raw-offset)/scale`), `value_map` (code → English **token**, what the wire carries) / `value_map_de` (German
display label, discovery only), `writable`, `min`/`max`/`step`. `process.Resolve` flattens
a report into `Point`s and expands `packData[]` into per-pack battery sub-entities;
unmapped properties still publish under `…/misc/`. `process.Item` is the single source of
the item path; `harender.Layout` (go-hamqtt `topic.SmartHome`) turns it into topics.

### HA discovery (`internal/hass`)
**Identity invariant (do not break):** `unique_id`, device identifiers and node ids are
built from `config.Config.IdentityRoot()` — `MQTT_TOPIC` if set, else `zendure2mqtt` — and
**never** from the topic name (whose default became `zendure` in 0.10.0). Pinned against
the frozen 0.9.0 documents (`testdata/frozen_v0.9.0_*.json`).
**Entity-ID invariant (do not break):** `default_entity_id` (not `object_id`, which HA
removed) is English/language-independent — `<platform>.<slug(deviceName)>_<english_topic>`;
the display `name` is localized (German when `LANGUAGE: de`), and select option labels too
(`value_map_de`). Battery packs are split into their own sub-devices via `via_device`. Rich
device registry: `serial_number`, `model_id` (= report `product`), `sw_version` (pack), and
`configuration_url` (device IP). **HA does not move already-registered entities to a new
device nor rename entity_ids** — a discovery-schema change needs a one-time reset (clear the
retained `homeassistant/.../config` topics, then republish).

Discovery is **device-based**: one retained document per HA device at
`homeassistant/device/<node_id>/config`, node id = the device identifier
(`<identity-root>_<sn>`, `…_pack_<packSn>`), carrying the device block, an
`origin` block and every entity as a component. Since 0.10.0 every entity reads
`value_json.val` and is available while `<name>/connected` ≥ 2 and
`<name>/status/<sn>/online` is true (`availability_mode: all`). It replaced 29 per-entity configs at the
four-segment `homeassistant/<platform>/<unique_id>/config` in ADR 0070 phase 5 step 5, with
`unique_id` and `default_entity_id` frozen. **The retraction of the old form comes first and
the ordering is load-bearing**: HA refuses a document while a per-entity config for the same
`unique_id` is retained, and says so with one `WARNING [mqtt.entity] Received a conflicting
MQTT discovery message` line and nothing else. `publisher.Config.LegacyEntityTopics` must
state `publisher.LegacyTopicByUniqueID` — the library default renders the five-segment form
and would retract nothing at all for this fleet.

### Virtual switches (`internal/virtual`)
`charge_active` / `discharge_active`: synthetic HA switches with no single backing property.
ON writes a property set (`acMode` + `inputLimit`/`outputLimit` + `smartMode`); state is
derived from the report (`acMode==1 && inputLimit>0`, etc.). Limits via
`CHARGE_ACTIVE_VALUE`/`DISCHARGE_ACTIVE_VALUE`. They flow through the normal publish +
discovery path as synthetic points; only the `/set` write is special-cased in the coordinator.

### Web UI + state (`internal/web`, `internal/state`)
Optional dashboard (`WEB_ENABLE`): the shared `github.com/SukramJ/go-bridgeui` module (server,
API, hardening, vanilla SPA — the same in go-mtec2mqtt and go-keba2mqtt). `internal/web` is
only the adapter: it presents the coordinator's `web.Backend` (`coordinator/web.go`, this
bridge's `state` DTOs) as go-bridgeui's contract, names the layout (live · packs · evcc) and
adds the labels in `internal/web/i18n/`. Writes go through `applySet` / `applyEVCCSet` — the
same path as an MQTT `set`, refusals logged under the same events and recorded in the store's
problem log; catalog entries with `confirm: true` (and the virtual switches) ask in the UI
first. The `state.Err*` write classes are go-bridgeui's. The coordinator feeds a thread-safe
`state.Store` (allocated only when the UI is on) with reports, reachability and problems.

### mDNS discovery (`internal/discovery`)
A tiny dependency-free mDNS browser (PTR/SRV/A/TXT parse with name compression) for
`_zendure._tcp`. Exposed via `zendure2mqtt-util discover`. Not yet wired into the local
backend as auto-discovery (and this SolarFlow does not advertise mDNS anyway).

### MQTT (`github.com/SukramJ/go-mqtt`, `github.com/SukramJ/go-mqtt/protocol`)
Custom pure-Go MQTT client (no third-party lib), TLS-capable, with a reconnecting `Lifecycle`
(used for the output broker). Since go-mqtt v1.0.0 the wire default is **MQTT 5.0**; the
output/local broker link uses that default, while the Zendure cloud link
(`internal/zendure/cloud/source.go`) pins `ProtocolVersion: mqtt.ProtocolV311` — the
third-party broker's MQTT 5.0 support is unverified. Shared module extracted from the twin
projects (formerly a per-repo `internal/mqtt` copy); includes the `TCPClient.ConnectionLost()`
channel that the cloud backend's reconnect loop reads.

### Topic layout (mqtt-smarthome 2.0, openccu-loom ADR 0083; since 0.10.0)
```
<name>/status/<sn>/<group>/<topic>             # retained status object {"val","ts","lc"}, QoS0
<name>/status/<sn>/battery/<packSn>/<topic>    # per-pack values
<name>/status/<sn>/online                      # true|false, device reachable
<name>/set/<sn>/<group>/<topic>                # subscribed at QoS1, writable entities + switches
<name>/connected                               # 0 (LWT/stop) | 1 (upstream unusable) | 2
<name>/info                                    # retained instance info
<name>/maintenance/set/{loglevel,restart}, <name>/maintenance/stats
homeassistant/device/<identity-root>_<sn>/config               # HA discovery, one document per device
homeassistant/device/<identity-root>_<sn>_pack_<packSn>/config # ... one per battery pack
```
`<name>` = `MQTT_TOPIC` (default `zendure`). Pre-0.10.0 layout (cleared by the sweep):
`<old>/<sn>/<group>/<topic>/state|set`, `<old>/bridge/status`.

## Config

Flat YAML (`config-template.yaml` documents every key); scalar keys overridable via
`ZENDURE_<KEY>` env (bool/int/float coerced). Loader: file → env → defaults → validate.
Key fields: `CONNECTION` (local|cloud), `LOCAL_DEVICES` (YAML list of SN+HOST), `REFRESH`,
`CLOUD_APP_TOKEN`/`CLOUD_TLS_VERIFY`, `MQTT_*` (incl. `MQTT_TLS`/`MQTT_TLS_CA`/`MQTT_TLS_INSECURE`
for the output broker: port default 8883 when TLS is on and `MQTT_PORT` unset, CA + insecure
refused, tls.Config built at startup in `outputBrokerConfig`), `HASS_*`, `WEB_*`, `LANGUAGE`,
`CHARGE_ACTIVE_VALUE`/`DISCHARGE_ACTIVE_VALUE`, `MQTT_MAINTENANCE`/`MQTT_STATS_INTERVAL`.
`ZENDURE_SUPERVISED` (env only) answers whether a supervisor restarts the daemon (the
maintenance restart is refused otherwise). Required: `MQTT_SERVER`. Missing local
devices or cloud token is non-fatal — the daemon starts and stays idle. `config.yaml` holds
credentials and is gitignored.

## Home Assistant add-on (`addon/` + `repository.yaml`)
`addon/config.yaml` is the options schema; `script/run.sh` maps options onto `ZENDURE_*` env
plus a generated `LOCAL_DEVICES` config file (env overrides only carry scalars). Multi-arch
images are built by `.github/workflows/addon-image.yml` (`ghcr.io/sukramj/go-zendure2mqtt-addon-{arch}`).

## Testing

Plain table/assert tests with hermetic inputs (no broker/device needed): `config` (env
override + validation), `process` (scaling / value maps / packData + topic scheme), `virtual`
(state derivation + write props), `cloud` (token decode + sign), `discovery` (mDNS parse incl.
compression), `web` (handlers + basic auth). MQTT-client tests live in the shared
`github.com/SukramJ/go-mqtt` module, not in this repo. Run with `-race` (`make test`).

## Release

Bump the version in **four** places in one commit: `internal/version/version.go` (`Version`
default), `addon/config.yaml` (`version`), `addon/Dockerfile` (`BUILD_VERSION`), and add a
`# Version X.Y.Z (YYYY-MM-DD)` section at the top of `changelog.md` (extracted by
`script/extract-release-notes.sh`). **Also** add a matching `## X.Y.Z` entry to the
Home Assistant add-on changelog `addon/CHANGELOG.md` (user-facing, HA-oriented summary;
call out any breaking add-on-option changes). Branch → PR → squash-merge → tag **`vX.Y.Z`**
(the `v` triggers `release-on-tag.yml`, `docker-build-push.yml`, `addon-image.yml`).
