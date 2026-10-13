# Vulnerability Traffic Evidence

The vulnerability details' "Associated Traffic" supports multi-page selection, purpose and description, sorting, and unbinding. The traffic page also supports multi-selecting records and associating them with an existing vulnerability at once. Inherited vulnerability evidence is read-only; modifications require entering the source task.

In system settings, "Agent Auto-Bind Traffic" is off by default, with the read and modify interface being `/api/settings`'s `agent_traffic_binding`. Enabling it increases token consumption from inspection requests/responses, tool calls, and prompts; the next Agent round uses the new setting. When off, auto-bind parameters and rebind tools are hidden, auto-bind instructions are not injected, and new submissions to running sessions are rejected. Manual binding, traffic capture, saved evidence reading, and export are unaffected.

When enabled, the default flow is: "Discovery added to inventory → Auto-trigger report Agent → Verify and bind traffic → Write report with latest evidence version". The reporter retains verification commands, key outputs, real traffic IDs, and purpose in `evidence`; the report Agent combines vulnerability details and execution records, verifies via `traffic_search` / `traffic_get`, then calls `bind_finding_traffic`, reads the latest `version`, and saves the report. When the switch is off, the report Agent does not gain original traffic search/read tools and does not auto-bind; it can still read manually bound snapshots and generate reports.

Backwards compatibility: `report_finding`'s `traffic_refs` / `evidence_hint_id` can still explicitly bind immediately. Binding is optional: for TCP and other non-HTTP vulnerabilities, or when no record is collected or found, omitting references still allows normal reporting and report writing; retain commands, logs, and other verifiable evidence, and explain the unbound reason; do not add mandatory fields. Submitted IDs must all be valid and the body complete. Any failure rolls back the binding operation; explicit bind-on-report failure rolls back the entire report. Repeatedly appending the same snapshot does not add binding or overwrite notes.

## Agent IDs and Report Versions

`report_finding` adds optional parameters; array order is the initial evidence order:

```json
{
  "traffic_refs": [
    {"traffic_id": "real-traffic-id", "role": "baseline", "note": "normal account request"},
    {"traffic_id": "another-real-traffic-id", "role": "proof", "note": "reproduction request"}
  ]
}
```

Purposes are `baseline` (normal control), `proof` (vulnerability proof), `verification` (supplemental verification), and `supporting` (supporting evidence, default). First verify real records via `traffic_search` / `traffic_get`; domain and time are only for candidate filtering and do not infer task ownership.

Prompt reference: [CyberStrikeAI vulnerability reporting tool guide](https://github.com/RuoJi6/CyberStrikeAI/blob/54d56774b8bd285817d16d48b70a4a5e6e0963f7/internal/app/vulnerability_tools.go); combined with this project's optional binding conventions. Do not introduce mandatory reason fields with no package; do not guess IDs; do not repeatedly probe only to supplement packages.

The first returned line remains `finding recorded: <exploration node ID>`; followed by JSON providing independent vulnerability record `finding_id`, exploration node `finding_node_id`, and binding summary.

- `get_finding_traffic(finding_id)` uses the **independent vulnerability record ID**, returning ordered list and `version`; pass `binding_id`, `side=request|response`, `offset`, `length` for segmented reads, max 8192 bytes per segment.
- `update_finding_report`'s `finding_id` **continues to use the exploration node ID**. Add `evidence_version` with the actual read version; evidence changes during generation will reject old version writes, requiring a re-read and regeneration.
- Old report calls without a version passed do not claim to overwrite existing traffic evidence. After binding, notes, purposes, or order changes, existing reports indicate pending updates.

When auto-bind is enabled, `add_hint` / `add_task_hint` supports saving `traffic_refs` in each element of a single hint or batch `hints`. Planner reports on behalf may pass `evidence_hint_id`, explicitly selecting references from this task's hints; cannot reference inherited hints. The system does not guess binding by domain, time, or browsing history. Failed submissions do not generate partial vulnerabilities or prematurely trigger reports.

Existing unbound vulnerabilities can be rebound with `bind_finding_traffic(finding_id, traffic_refs)` without re-registering. `list_findings` / `list_task_findings` / `node_detail` / `get_task_node_detail` return explicit `finding_id` and `finding_node_id`; old `id` retains exploration-node semantics.

At startup, only optional properties are added to old tool schemas; original default traffic tool binding extends to the report Agent; rebind tool defaults to the report Agent. Custom binding lists, prompts, descriptions, and enabled states are preserved. Final unified tool assembly instructions: the reporting role handles evidence handoff, the report Agent handles verification, binding, and report writing. When platform conversations lack task context, structured prompts should hand off to the task Agent rather than reporting directly. Before judging task completion, existing evidence should be handed off; no package does not force waiting. Failed `report_finding` does not trigger the report Agent.

## API

Base path: `/api/exploration/findings/{finding_id}/traffic`, using the independent vulnerability ID. Uses existing authentication; `context_task` verifies task visibility and inheritance read-only.

| Method / Relative Path | Request / Response |
| --- | --- |
| `GET` | Ordered summary, evidence version, report adopted version |
| `POST` | `{"traffic_refs":[...]}` batch append |
| `PATCH /{binding_id}` | `{"version":1,"role":"proof","note":"note"}` |
| `DELETE /{binding_id}` | `{"version":1}` |
| `PUT /order` | `{"version":1,"binding_ids":["2","1"]}`, must be full list |
| `GET /{binding_id}` | Snapshot metadata and bounded body preview |
| `GET /{binding_id}/body` | `side`, `offset`, `length`; `download=1` for full original bytes |

Version/sorting set conflicts, writes to archived items return `409`; inherited writes return `403`; non-existent or non-vulnerability bindings return `404`; traffic/attachment reads and verification failures return explicit errors.

## Storage and Migration

Startup idempotent PostgreSQL migration: add `traffic_evidence_snapshots`, `finding_traffic_bindings`, and `findings.evidence_version` / `report_evidence_version` (default 0). Do not guess rebinds from historical text.

Snapshots save original traffic ID, collection time, URL, method, status, request/response headers, body length, and SHA-256. Bodies are stored by hash at `<data>/evidence/blobs/<first two chars>/<hash>.bin`, independent from cleanable `data/traffic`; multiple vulnerabilities can share snapshots/bodies. Snapshots do not provide content update interfaces; read and export fail on hash mismatch.

Full body read under original traffic write lock, including large body blobs and old directory records. Persist and verify files first, then write exploration nodes, intent relations, vulnerabilities, snapshots, and bindings in a single PostgreSQL transaction; notify planner only after commit. Failures may leave unreferenced files but do not produce partial business records.

PostgreSQL advisory lock `7337741004` coordinates evidence files and SQL references; task row locks prevent evidence modifications after archive queuing. Recovery from body install to metadata commit maintains evidence lock throughout. Deleting a vulnerability cascades to remove bindings.

Cleaner runs hourly, only reclaiming unreferenced and non-in-progress content, at least 24 hours delayed. Normal traffic cleaning does not touch the evidence directory. Hot data backups must include both PostgreSQL and `data/evidence`.

## Export and Archive

Markdown includes ordered evidence list and version; JSON includes metadata; CSV adds count and binding ID. `md-zip` retains vulnerability Markdown and provides:

```text
evidence/<finding_id>/<binding_id>/
  manifest.json
  request.http
  response.http
  request.bin
  response.bin
```

Markdown references messages via relative links. Before sending downloads, complete attachment copying, hash verification, compression, disk sync, and full ZIP entry CRC verification; missing/damaged entries cause the entire download to fail. Full attachments retain original binary bytes.

Archive v3 collects snapshots and bodies by vulnerability binding relations, not depending on original traffic or domain. Package verification completes before cleaning hot data; shared evidence continues to be retained. Recovery first verifies body installation, then transactionally restores metadata and bindings, supporting retry on failure. v1/v2 remain recoverable; missing new fields are explicitly filled to 0.

## Verification and Boundaries

Each test package uses an independent, new PostgreSQL test database specified by `ARTEX_PG_DSN`, avoiding residual task/model fixtures triggering background runs. Run full package tests and confirm no skips from missing configurations:

```sh
# Set ARTEX_PG_DSN to the corresponding independent test DB before each package run; explicit config failure must error.
go test ./<package> -count=1
go test -race -p 1 ./evidence ./db ./agent ./server -run 'TestEvidence|TestFindingTraffic|TestFindingEvidence|TestReportFindingAtomicContract|TestTaskArchive'
```

Frontend verification includes `npx tsc --noEmit`, Biome checks on affected files, Webpack build, and `NEXT_EXPORT=1` static export. Use independent cache directories to avoid overriding running dev services.

Local end-to-end acceptance uses independent ports, controlled HTTP / domain HTTPS targets, and temporary data directories, covering both binding entry points, multi-page selection, sorting/notes, error prompts, inheritance read-only, download, and deletion of original traffic followed by export, archive, hot body reclaim, recovery, and hash verification.

The first version uses a global evidence coordination lock; during large batch binds/exports or slow attachment downloads, other evidence operations may wait. Missing recorded entries or full bodies cannot fabricate evidence. This feature does not change the capture switch and does not handle HTTPS direct IP access certificate issues.