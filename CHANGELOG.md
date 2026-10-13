# Changelog

Important changes to this project are recorded in this file, following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.3.15] - 2026-10-07

### Interception

#### New features

- **New built-in intercept rule "delete-type interface paths"**: the built-in HTTP destructive rule used to match only the DELETE **method**, while most apps' delete endpoints trigger on GET/POST — a call like `curl 'http://t/api/user/delete?id=1'` would really delete target data. A new `deny` rule now covers `/delete /del /remove /unlink /erase /destroy` (a separator must follow the verb, so `/delivery`, `/details`, `/delta` are not false-matched). Existing instances get it on upgrade; disable or delete it under "System → Command Interception".
- **Standalone token-usage view for model-fallback approvals**: approval calls previously had no usage attribution of their own — spend was guesswork. They are now metered separately, with a usage card under the "Model-fallback approval" switch in "System → Command Interception" — call count, input, output, and cache read/write totals, plus a mini bar chart of daily spend over the last 30 days.

### Finding push

#### New features

- **Findings can be pushed to IM**: a new "System → Notification Push" page sends scan findings to six channel kinds — **DingTalk, Feishu, WeCom, generic Webhook, Telegram, email**. Any number of instances per kind, each with independent enable/disable, rate limits, and filter rules.
- **Realtime and digest delivery modes**: realtime mode sends each hit immediately; digest mode merges hits into one message per global period (default 30 minutes), leading with new-count and severity distribution. For "critical in realtime, the rest digested", create two channels and configure them separately.
- **Filters in four dimensions**: minimum severity, task/asset scope limits, finding-type keyword include and exclude (exclude wins), and whether to receive disposition-change events (off by default). Messages carry a "View details" button whose target comes from the global "callback base URL" setting — no button when left empty.
- **Delivery history and manual resend**: lists each delivery's status, attempt count, failure reason, and owning channel, filterable by channel and status; failed items resend in one click (resending zeroes the retry count).
- **Masked credential echo**: webhook URLs, signing secrets, bot tokens, SMTP passwords and the like echo back only masked values with a trailing-digits hint; submitting them back unchanged means "no change", clearing the input deletes the field.

#### Fixed

> This section is the outcome of a security audit round after the feature's first version — every item was reproduced first, then fixed, then covered by a regression test.

- **Fixed mask bypass via "change target address, keep credentials" (severe)**: config merging kept the stored value for any "unmentioned key", so changing only the address while staying silent about credentials made the server send the real stored credentials to an arbitrary address, completely silently. Now, once the target address changes, every credential field must take an explicit stance — a new value, or an explicit blank meaning no longer needed.
- **Fixed zero escaping of untrusted content on markdown channels**: finding titles and summaries come from model output and asset URLs from scan results; a crafted title renders as a clickable outbound link in DingTalk / WeCom / Feishu, and image syntax gets fetched by clients at render time, leaking reader IPs. All channels now single-line + escape markdown metacharacters, with escaping at each channel's render exit rather than in the shared title function.
- **Fixed the SSRF surface on delivery addresses**: loopback, intranet, and cloud-metadata addresses were all deliverable, while the first 200 bytes of failure bodies flowed into `last_error` and were echoed by delivery history — a semi-blind read primitive. Defense now sits at **dial time**, covering DNS rebinding and rejecting cross-host redirects; loopback and link-local addresses need an explicit `ARTEX_NOTIFY_ALLOW_LOCAL=1`, while RFC1918 private ranges stay allowed (self-hosted intranet SMTP relays are common).
- **Fixed push credentials leaking through error messages**: DingTalk's `access_token`, WeCom's `key`, and Telegram bot tokens all sit in URLs, and on delivery failure the full URL flowed into `last_error`, delivery history, server logs, and frontend notices. All are redacted now, keeping only `scheme://host` plus the underlying cause.
- **Fixed silent loss when a truncated digest batch was marked fully delivered**: truncated findings appeared neither in the message nor in the failure list, yet history showed success. Batching is now per-**message**: only the items packed into the current message are marked, the rest return to the queue for the next message without consuming retries, and the message header states how many more items continue in the next message.
- **Fixed a typo in the minimum-severity threshold silently disabling the filter**: a typo like `min_severity` = `hgih` gave the unknown severity ordinal 0, degrading the check to always-true — the user thought only critical findings would arrive while every finding flooded the group. Values are now validated on the write path with the allowed values listed.
- **Fixed `rate_per_min = 0` (unlimited) being unreachable**: the persistence layer quietly rewrote an explicit 0 to the default, so operators thought they had lifted the limit while still being throttled. Defaults are now filled at the API layer when the field is absent.
- **Fixed digest channels fully bypassing the token bucket**: `rate_per_min` previously did nothing in digest mode. Claimed counts are now bounded by both the round quota and the in-memory cap.
- **Fixed failure handling judged by the batch's max attempts, dragging new deliveries down with old ones**: one twice-retried old delivery would pull brand-new deliveries in the same batch into failed, permanently losing new findings without a single retry. Now decided per item.
- **Fixed undeliverable snapshot entries in digest batches silently marked successful**: such entries were skipped at render time yet counted as success with the batch. They now explicitly fail with a reason.
- **Fixed per-round per-channel delivery counts possibly exceeding the lease window**: in multi-instance deployments, lease-expired rows get reclaimed, re-sent, and double-increment attempt counts. The per-round cap is now derived backwards from "lease / single-attempt timeout", with an assertion pinning those constants' relationship.
- **Fixed Telegram truncation possibly cutting HTML entities**: truncation avoided half tags but not entity fragments like `&amp`, which parsers may reject the **entire** message over. Both are avoided now.
- **Fixed the email channel judging transient SMTP failures as permanent**: greylisting `450`-style 4xx is a temporary refusal that should retry later, but everything was judged fatal — servers with greylisting failed every push on first attempt. Now distinguished by reply-code first digit: 4xx retryable, 5xx permanent.
- **Fixed submitting nested structures really writing mask literals into the DB**: object fields like `webhook.headers` can only be masked or submitted whole — stuffing mask sentinels inside the object stored them as real values and silently broke auth. Such submissions are now explicitly rejected.

#### Design notes

- **The finding-write transaction does exactly one blind INSERT**: `notification_events` is written in the same transaction as the finding, so commit guarantees both atomically. This INSERT deliberately reads no channel tables and runs no user filter rules, and is wrapped in a `SAVEPOINT` — otherwise one misconfigured filter could abort the transaction and keep critical findings out of the DB.
- **Delivery claims via lease, not long transactions**: `FOR UPDATE SKIP LOCKED` claims a row, flips it to `sending`, and pushes `next_attempt_at` into the future as the lease — no DB lock is held during delivery; rows orphaned by crashed processes get reclaimed after lease expiry, self-healing without infinite retries.
- **Rate limiting does not consume retry budget**: the round quota is computed from the token bucket first, then that many rows are claimed. Reversing the order would burn one attempt on deliveries blocked purely by throttling, draining the three-attempt budget on waiting and landing in failed.

### Test infrastructure

#### Fixed

- **Fixed permanently leaked assets from cleanup ordering in the company-ICP-attribution test**: `defer d.Close()` ran at function return before `t.Cleanup`, so all cleanup statements landed on a closed connection, errors were dropped, and test assets plus companies stayed in the DB forever — also mysteriously failing other asset-count assertions. Closing the connection now goes through `t.Cleanup` registered first, and cleanup failures surface.

### Traffic

#### New features

- **Traffic list supports content search, path / status-code / length filters, and column sorting**: the filter row filters by **response content** (full-text index), **path**, **status code** (exact value or buckets like `2xx`), and **response length range**; headers sort by time, status code, and response length, with sort preference stored locally. Two new indexes take effect automatically on upgrade for existing instances; Agent tool query contracts are unchanged.
- **Traffic list gains "Clear all"**: deletes every traffic record at once (ignoring current filters) and cleans up index history host directories no longer referenced, finishing with a full compaction and a report of actual space freed. Traffic evidence bound to findings lives in a separate evidence store and is unaffected.

#### Fixed

- **Fixed disk space never freed after deleting traffic**: the index DB was created without `auto_vacuum` so the file never shrank; the full-text index is also `contentless_delete`, where `DELETE` only writes tombstones without reclaiming postings — deleting traffic made the index bigger (measured: 6MB of captured bodies → 16MB index, still 16MB after deleting everything). New index DBs now enable `auto_vacuum=incremental`, with background chunked full-text merges plus incremental reclaim after each delete gradually returning space to the system (same scenario falls back to 104KB); reclaim releases the write lock between chunks and never blocks traffic recording.

> Upgrade note: `auto_vacuum` can only be set at DB creation, so **existing instances' index DBs stay in the old mode** and incremental reclaim is a no-op on them (a notice line is printed at startup). Tombstone growth on such DBs has already stopped after upgrade; reclaim the occupied space once with "Clear all" — that step converts the DB to incremental-reclaim mode, after which everyday deletes take effect on their own.

### LLM

#### Fixed

- **Fixed the custom session header not applying on the "one-shot LLM call" path**: target decomposition (round 0) and cold-node compaction are one-shot calls without a transcript store, so the context carries no session id and gateways never see the header — on endpoints like opencode zen that 400 on a missing header, this showed as "round 0 fails while later planner rounds work fine". Both paths now explicitly carry an exploration-stable session id, which also attributes token usage to the right exploration (previously unattributed).

### Tools

#### Fixed

- **Fixed MCP and custom-tool output truncation not applying, flooding context with huge results**: MCP tool returns previously bypassed truncation entirely, while HTTP-style custom tools passed an empty tool context and always used a hardcoded 6000 characters instead of the session-configured cap. Both paths now go through `actool.Capture`: truncate at the session `MaxOutputChars` (default 30000); with `ToolOutputDir` set, full output spills to disk while the message keeps only the leading fragment plus a file pointer.
- **Upgraded norma to v0.4.3**: the harness layer uniformly executes `CaptureOnce`, so no tool (including future ones) can blow up context by missing truncation again.

### Deployment

#### Fixed

- **Fixed the activity stream not connecting behind a reverse proxy**: the SSE address was unconditionally built as `${hostname}:8787`, so in deployments proxied to 443 the page loaded from `https://domain/` while SSE pointed at port 8787, which is not open to the public — the activity stream reconnected in a loop. Now split by `NODE_ENV`: same-origin by default in production, `:8787` kept only for `next dev` to bypass Next.js SSE buffering; the `NEXT_PUBLIC_SSE_BASE` override stays. The README gains reverse-proxy deployment notes.

### Findings

#### Fixed

- **Fixed the "by asset" view asset list overflowing with no scrollbar**: the outer card only set `max-height` without a definite height, so the scroll viewport's percentage height could not resolve — with many assets the list either burst the card or got clipped without scrolling. The height cap now lands directly on the asset tree's native scroll container and adapts to window height.

### Database

#### Fixed

- **Capped the connection pool**: `database/sql` puts no limit on connections the app itself may open — with no idle connection in the pool it unconditionally creates new ones until PostgreSQL's `max_connections` (default 100) refuses. Now capped at 32; excess queries wait for an idle connection instead, so the same load shows as slower rather than erroring; lower it together if `max_connections` was tuned down.
- **Improved connection reuse**: `MaxIdleConns` defaults to 2, so beyond 2 concurrent connections each used connection closed on return and the next request redid TCP plus PostgreSQL auth handshakes. The idle cap now matches the total cap, plus a 30-minute connection lifetime and 5-minute idle eviction so the pool naturally rotates long-lived bad connections.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.14] - 2026-09-24

### Task list

#### New features

- **Task list gains a "Findings" column**: counts in Critical / High / Medium / Low bands, non-zero bands colored by severity — each task's finding scale and distribution visible at a glance.

#### Changed

- **"Description / Target" columns narrowed**: overlong content ellipsizes with full text on hover, reducing long text crowding the list horizontally.

### Exploration graph and planner overview

#### Changed

- **Slimmed the planner overview (graph_overview) and capped list sizes**: recent facts, finished intents, pending intents, cold-zone summaries, and confirmed-finding details all become "latest window + count backstop", queryable on demand when not shown; significantly shrinks per-round LLM context and avoids context bloat on long tasks (the related task's cold overview is throttled too).

#### Fixed

- **Removed the reverse duplicate edge between digest and finding in the exploration graph after collapsing cold nodes**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset interception

#### New features

- **New global asset intercept rules (blocklist)**: record exact- and fuzzy-match domains / IPs / URLs plus CIDR ranges, with rule CRUD and enable/disable, managed at "System → Asset Interception"; ships with fuzzy blocking of government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) sites built in.
- **Asset interception joins the execution chain**: before dispatching intents (`add_intent`) and inserting assets (`insert_assets`), Agents run an intercept check on target assets — intents hitting a block are not dispatched, assets hitting a block are not inserted, and the Agent gets the asset info plus the block reason back.
- **New task-level block / allow (allowlist) rules**: independent of global rules and effective for the current task only, judged "block first, then allow" — a block hit forbids; with no block hit but allow rules configured on the task that all miss, the verdict is "testing not allowed"; with no allow rules configured, the allowlist stays off. Enterable at task creation and editable (CRUD + enable/disable) in the task detail "Overview".
- **Task templates support preset categories and task-level block/allow rules**: templates can save a task category plus a set of task-level rules, carried into the new-task form when applied.

### Operation review

#### Fixed

- **Tightened the model-judge output protocol to cut truncation-caused false allows**: the verdict explanation (comment) cap drops from 500 Chinese characters to 120 and forces "JSON only, no preamble or code fences", so verdicts can no longer be cut off by `MaxTokens` into unparseable output that then allows through under the model-failure policy (fail-open).

### Task archives

#### Fixed

- **Archives skip symlinks instead of failing the whole package**: end-to-end archive format supports only regular files and directories, and any symlink in the workdir used to fail the entire task archive; now the symlink is skipped with a log line while remaining files archive normally (links are not followed, never escaping the tree).

### Accounts and compliance

#### New features

- **New "Terms of Use and Disclaimer" dialog before login**: login requires checking the agreement box first.

### License and dependencies

#### Changed

- **The project adopts the AGPL-3.0 open-source license**, with the README's license and disclaimer notes completed.
- **Upgraded norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Exploration timeline board

#### New features

- **Task detail page gains an "Exploration Timeline Board"** (#144): a chronological stream view of task exploration nodes (origins/targets/intents/facts/findings/hints/compactions) with type filtering, keyword search, ascending/descending order, paging, and auto-refresh; sitting on "newest-first page one" is the live slot that refreshes every round, while leaving it only accumulates an unread count without disturbing current reading, plus a one-click "N new items · back to latest" return. Day grouping keeps long-task paging oriented ("which day was this"). 
- **Each board row shows the node id** (#145) for cross-referencing the exploration graph and pinpointing nodes.
- **Board row expansion shows upstream/downstream plus anchored assets** (#147): expanding an item reveals its upstream (where it came from) / downstream (what it produced) relations, with hover cards on related entries (type/status/source/time/summary/payload fragment); the node's anchored assets are listed alongside (type tag + recognizable text). Related data ships with the board page — expanding sends no extra requests.
- **Board search filters by node id** (#150): beyond "content / source", the search box matches node ids — a plain number or the UI's "#41" form pinpoints the node exactly.

### Intent management

#### New features

- **Intent deletion supports "soft / hard delete" modes** (#149): pending, running, and paused intents are all deletable, with a required reason; the mode is chosen in the confirmation dialog.
  - **Soft delete (default)**: the intent becomes "deleted" with the reason in a dedicated field, keeping the intent node, all its output, and lineage.
  - **Hard delete**: physically removes the intent plus "exclusively supported" descendant nodes (cascading along output/intent chains to leaves), avoiding orphaned data; the deleted intent's token accounting is archived under its original dates; shared nodes (still referenced by other intents), targets, and task-root facts are always kept, and the hard-delete dialog previews the expected cascade node count.

  Both modes notify the planner that "the user deleted this intent + reason" for replanning.

### Operation review

#### New features

- **Approval records filter by status and decision source** (#139): filter approval records by approval status and decision source to locate records fast.

#### Fixed

- **Pending approval requests load independently of history paging** (#133): pending requests are no longer affected by history-list paging and stay fully visible while browsing history.

### Agent

#### Changed

- **Worker role description switched to generic security-platform wording** (#138).

#### Fixed

- **Fixed cold-node compaction never triggering**: the per-task planner was never wired to the Compactor, so cold-node compaction (cold-digest) never ran — now restored.

### Chat

#### New features

- **Chat supports @-mentioning multiple record types with scroll paging** (#135): @-mention several record kinds in chat, with scroll-paged mention candidates.

#### Fixed

- **Long message bubbles stay inside the conversation panel** (#137): overlong message bubbles no longer overflow the conversation area.
- **Fixed "missing upload file" when uploading before a conversation exists**: the composer's file picker now snapshots the `FileList` into an array before clearing the input and then calls back; previously the draft state had to create the session asynchronously first, and by resume time the live-bound `FileList` on the input had been cleared — uploads missed the file field and the backend returned 400.

### Traffic

#### Fixed

- **Traffic recording proxy listens on 127.0.0.1 by default** (#129, #130): avoids exposing an open proxy on other interfaces under default config.

### Web

#### Fixed

- **Fixed the static-export task list page losing its global header**.
- **Filled in the demo finding-detail associated-traffic mock, fixing a white screen**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation review

#### New features

- **Approval records support source pinpointing** (#125): an approval record's "source" jumps back to the tool execution that triggered it — opening the original full session, auto-paging to the target, expanding the command and result in place, and centering it in the scroll area while keeping surrounding messages readable; auto-recentering stops once the user scrolls manually. Across plain chats, task Workers, planners, and Main-Agent session segments, precise location works through persisted `tool_use_id` plus task mapping (new in-scope call-ID index); missing, duplicated, or ambiguously associated entries prompt explicitly instead of jumping elsewhere. Deleted sessions or archived tasks / missing records get a clear notice.
- **Approval records support paging** (#115).

#### Changed

- **Slimmed model-review input** (#124, #125): model review (the LLM judge after rules miss) input converges to a versioned JSON of "this one complete tool call + explicitly selected short background + local workdir". Background takes only real user messages (chat / task-Main-Agent current user messages); Workers no longer carry intent summaries and inherited background from parent Agents is cleared; planner and auto-triggered sessions do not fabricate user messages. Task descriptions, targets, operation constraints, global exploration posture, historical calls, and full Worker intents are no longer attached — they still feed Agent execution and standalone session audits as before, just not operation review. Every verdict must output JSON `decision`/`comment`, with the explanation covering "actual operation, post-success consequences, hit rules"; instructions inside params or background cannot change review policy. Snapshots plus fingerprints of the actual model input are saved; old snapshots stay versioned and are never backfilled with current data.

#### Fixed

- **Model verdicts wrapped in code fences no longer silently allow** (#126): when the model's verdict JSON arrived wrapped in ``` fences, parse failure used to allow it through silently under the model-failure policy; now fence markers are stripped before parsing, and only a still-failing parse falls through to the configured failure policy.

### Agent

#### New features

- **New experimental noa context compaction** (norma upgraded to v0.4.0): an opt-in experiment in system settings, off by default. When on, noa (model-driven context compaction) takes over context compaction for the four Agent kinds — Main Agent, planner, Worker, chat — replacing the built-in compactor; compaction originals persist in archives centralized under `<workDir>/noa/<session-id>/` (not scattered across task dirs), one globally unique directory per session ID. Hookup failure falls back to built-in compaction automatically without interrupting real tasks; the switch is read once per run, so toggling only affects later-started runs.

#### Fixed

- **Exploration-graph tools reject without task context instead of nil-crashing**: calling exploration-graph tools outside a task context returns a clear error instead of crashing on an empty-store dereference.

### MCP

#### New features

- **Support for legacy SSE MCP servers** (#117): compatible with MCP servers offering only legacy SSE transport.

### Traffic

#### Fixed

- **Traffic search matches record hosts port-sensitively** (#114): `traffic_search` host matching now includes the port, so same-host records on different ports no longer cross-talk.
- **`traffic_search` description migration errors no longer break later reporter migrations**: a single failed migration is isolated and later migrations still run.

### Web

#### Changed

- **Finding-retest option list gains separators** (#122): separators between retest-finding choices for clearer visuals.

#### Fixed

- **Fixed the demo task-detail page crashing whole**: in mock mode the task-detail "Sessions" tab fetched `GET /api/tasks/<id>/side-questions` (side-question history), but the mock handler had no such route — the read fallback treated "path ends in s" as a collection and returned `[]`, leaving `data.items` as `undefined`, and the side hook's `merge()` threw `TypeError: t is not iterable` while iterating it; the exception fired inside a `setItems` updater, which React defers to rethrow at render time, so the caller's `catch` could not grab it and the whole page fell to the error boundary showing "This page couldn't load". The mock handler now explicitly returns empty side history, and `sideAPI.history` normalizes the return (`items` non-array always coerced to `[]`) as defense in depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Network

#### New features

- **Network search gains an official DeepSeek source**: directly reuses the currently active LLM config. It differs in kind from the other three sources — DeepSeek has no directly callable search API; search lives only inside its Anthropic-compatible interface (the `web_search_20250305` server-side tool), executed by the DeepSeek server. So it supports **official DeepSeek endpoints + anthropic protocol only** (OpenAI-protocol endpoints reject server-side tools outright), each search additionally **costs one model call**, requests **bypass the search egress proxy** and **leave no traffic trace**, and results carry **only titles and links** (no snippets — fetch body text via WebFetch when needed). The settings page documents these limits but **does not block on validation** — users confirm eligibility themselves and can verify with a live run of the "Test search" button.

### Agent

#### New features

- **Workers gain cross-work hindsight**: new `search_all_worker_traces` (global keyword search across all works' execution in the task without knowing intent_id first, for reusing observations another work saw but never wrote into facts) and `get_worker_trace` (after pinning a work, list its step flow, search in place by keyword, fetch full content by step_id) — reuse what other works observed to avoid duplicate labor.
- **`node_detail` handed down to workers**: with the hindsight tools above, a worker holding an intent_id / node id can directly fetch that node's full details.
- **`add_hint`-triggered planning rounds are explicitly announced**: new hints used to fold silently into the posture overview for the planner to notice alone; now one `add_hint` logs the planner a "human added N strategy hints: …" trigger (batched as one, never per-hint spam), the planner is told outright "this round was triggered by new hints" and sees the hint contents directly.

#### Changed

- **Global posture prompt switches to an "unleashed" tone**: encourage divergent exploration and timely cross-intent clue reports instead of converging too early.
- **Slimmed worker boundary wording**: first blockage is not proof of exhaustion — focus on "exhaust the reasonable bypasses inside this intent before concluding".
- `insert_assets` drops the per-asset `related` param: its only job was deciding whether to add the asset into the current task scope, the value was never persisted (voided when the same asset re-registered, invisible in the UI as to who was judged irrelevant) — in practice it just made the model render one more unkeepable judgment.
- **Test scope (`task_scope`) no longer gated by the asset-coverage switch**: `insert_assets` auto-scoping (`source='auto'`) runs whether coverage is on or off, and `add_task_scope` stays available to planner / in-task Main Agent / target-decomposition Agent. Scope is the task's authorization boundary and the asset-query filter baseline; the coverage switch only decides whether to use it as the denominator for metrics — never whether to accumulate scope itself. Previously, turning coverage off killed both `auto`/`agent` write paths, leaving `task_scope` with only hand-added UI rows. `list_untested_assets` stays hidden when coverage is off (it is purely a coverage view).

#### Fixed

- **Fixed cross-task asset mixing** (#59): `list_assets` used to hardcode task id 0 and bare-query the whole shared asset store, while the model had no way to write scope filters — so other tasks' assets (especially IPs) were mistaken for the current task's targets and testing veered off. Now it returns only assets inside "this task and directly linked tasks" test scope (`task_scope`), matched by **ownership** rather than literal value: a root domain in scope reveals all its subdomains/services/endpoints, a CIDR in scope reveals in-range hosts and services; out-of-scope assets are unfetchable even by direct id. Non-task contexts (Auto/pentest) with no scope to honor still fall back to the whole store. Also fixed the ownership blind spot where directly connected IP hosts (e.g. `http://1.2.3.4/api`) missed CIDR-scope matching. The UI "Test Assets" view (filtered by task producer) is unaffected.
- **Fixed worker cross-work hindsight tools accidentally unbound**: after `search_all_worker_traces` / `get_worker_trace` (plus `node_detail`) joined the worker default toolset, an old "converge the worker tool surface" migration unbound them again at startup, leaving workers without these tools. They are removed from that unbind list, with a one-time rebind for DBs that already ran the old migration.
- **Fixed occasional `/btw` side-question persistence failures**: the side_question checkpoint now strips the JSONB-unsupported NUL (`\u0000`) escape before writing, so content containing that character no longer errors on write.

### Triggers

#### Fixed

- **Merged trigger sessions dedupe task descriptions/targets per task**: multi-task merged triggers no longer repeat the same task's description/target into the message, avoiding long targets stacking up and blowing out context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### New features

- **In-task Main Agents support multiple sessions**: create, switch, and independently reset contexts; every session is interactive.
- **Persistent `/btw` side questions**: follow-ups without interrupting the main line, Q&A persisted, still there after restarts.
- `spawn_task` gains `source_task_ids` so new tasks **read-inherit** assets and findings from source tasks.

#### Changed

- Shut off worker cross-engagement memory and converged its default tool surface: reading context and cross-work review are planning duties; workers handle single-intent execution and writeback only.
- Slimmed the worker default prompt; `get_worker_output` drops the `terminated` and `worker_name` fields; slimmed the `insert_assets` / `list_assets` descriptions.

#### Fixed

- Fixed works cut short by **idle rounds**: models sometimes spend a whole round emitting only thinking — no text, no tool calls — which the harness reads as a natural end (`end_turn` with no `tool_use`) and closes out as `completed` with an empty summary — an unfinished intent dying mid-way, showing as "task ended normally with no text summary". None of the five LLM retry layers can reach it: it is not an error, the same-provider safety-window retry enters on "stream failure", the breaker judges `err == nil` a success, intent rerun only recognizes `model_error`; the SDK empty-response retry cannot reach it either, since that layer judges emptiness by "whether any event was ever yielded" and thinking deltas are events. Now the work Stop hook recognizes such rounds and injects a continue-running instruction so the model proceeds with its already-produced thinking — deliberately not "resend as-is", because such idling is usually set by prompt and context shape (stable behavior, not random flakiness); resending would only make the model think it all over again.
- Counts reuse the LLM page's **empty-response retry count** under "Retries and backoff" (both answer "the model finished but produced nothing substantive", differing only in criterion and means), default 2, `-1` to turn off and revert to the old "idle ends it" behavior. It caps an intent's **total**, not consecutive count: the harness already limits consecutive idling to a single nudge, refreshing the quota only when a real tool round happened — so this number blocks loops like "tool → idle → nudge → tool → idle" from draining the intent budget. On trigger, logs record `[work <worker> · #<意图>] idle round…injecting continue instruction (n/N)`. Design: LLM-retry design §1.1.
- Fixed long-conversation `/btw` context budgets and input layout; non-secure contexts (non-HTTPS access) fall back to generated side-request IDs.

### Traffic

#### New features

- Findings support **binding multiple traffic evidence items**, sortable, annotatable, distinguished by role (request/response/supporting).
- The report Agent **auto-binds** related traffic evidence before writing reports.
- New Agent **traffic-binding switch**, plus evidence handoff between Agents completed.

### Tasks

#### New features

- **Conversation-scoped finding retests**: retests run in standalone Agent sessions, with running status in list and detail views.

### Interception

#### New features

- **Approval records gain detail and execution audit**: tool request context, model/rule first verdicts, execution output, and param fingerprints are viewable.

### Assets

#### New features

- **Task test assets support DSL search**.

### LLM

#### Fixed

- Fixed test-connection not resending the custom session header, which made opencode zen return 400.

### Network

#### New features

- **MCP HTTP transport supports skipping TLS verification** for self-signed services.

### UI

#### New features

- Session list groups by Agent with expand/collapse and independent pinning; conversations filter by Agent.
- Attack-chain graph renders compaction (digest) nodes and collapses their members.

#### Fixed

- Fixed white screens from out-of-sync login credentials.
- Interception prompts now clearly mark "platform control" to avoid misreading them as target-side defenses.

### Deployment and updates

#### New features

- **One-click in-page updates**: the system settings page gains a "Version & Updates" card, with the top bar lighting up when a new version exists. Flow: download release bundle → verify `SHA256SUMS` → smoke test → stage → exit for the supervisor script to relaunch and complete the swap, with the page auto-refreshing.
- New supervisor launch scripts `start.sh` / `start.bat` as the official entrypoint (release bundles and Docker images already carry them; `install.sh` unchanged), relaunching by exit code and forwarding SIGTERM to artex. Verify/swap logic all lives in Go; the scripts stay dumb.
- Automatic failure fallback: failed verification or smoke tests discard the candidate and keep the current version running; three consecutive failed starts of a new version roll back to the previous one. The settings page also offers manual rollback (note: DB schema never rolls back).
- Updates only trust the GitHub domain over mandatory HTTPS; the release source is not configurable; dev builds disable one-click updates. GitHub query results cache for 30 minutes so top-bar hints never exhaust API quotas.

#### Known limitations

- Under Docker only the program swaps, not the image: the toolchain does not upgrade along, and rebuilding the container reverts to the image's built-in version — still use `docker compose pull artex` when needed.
- `skills/` from release bundles is not synced, so newly added built-in skills in a new version do not auto-activate.
- Updating restarts, interrupting running tasks.

### Dependencies

#### Changed

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### New features

- LLM page gains a "Retries and backoff" tab: **counts** and **intervals** for all five retry layers are configurable. One model call's failure passes from inside out through connect retries (SDK, pre-stream connection resets / timeouts / 429 / 5xx), empty-response retries (SDK, clean finish with zero content, openai format only), same-provider safety-window retries (broken-stream replay before any output reached the caller), pool breaker (skip-after-threshold consecutive failures with cooldown), and intent rerun (whole-intent rerun after a worker ends `model_error`) — inner layers exhaust before outer ones engage. Two knobs per layer with uniform semantics: blank = original default counts with exponential backoff; a count uses that count; an interval swaps exponential backoff for a fixed interval; -1 = turn that layer off. The first three layers travel with the endpoint and can override global defaults per field in each model config (pinning only the interval still inherits the global count); breaker and intent rerun are process-level semantics with a single global copy. Hot-applied on save, no restart needed. All-blank equals current behavior, with old-DB upgrades byte-identical (new columns default 0, absent settings keys mean all-defaults). Connection testing deliberately skips these params — its 30s hard timeout plus user-configured retries would turn working endpoints into timeout failures. Design: LLM-retry design.
- Each LLM config supports a custom **session header** (`session_header_key`): when non-empty, every LLM request carries that HTTP header with the current run's session id (`conv-<id>` for chats, `exp<x>-worker-i<intent>` for workers, etc.) — for gateways doing prompt caching / sticky routing on a session-id header. Implementation reads the session id from the request context for injection, no norma changes needed, and one shared provider can emit different header values per session. Old DBs migrate; the frontend LLM config dialog gains an input box.

#### Fixed

- Fixed saving LLM configs erroring `23502` on an empty session-header field: the column is `NOT NULL DEFAULT ''`, but a misapplied `NULLIF($n,'')` wrote unfilled headers as NULL against the not-null constraint — now passed through with empty-string semantics.

### Agent

#### New features

- **Wall-clock timeout now winds down in place**: on norma v0.3.6, `MaxDuration` interrupts the running tool and continues on the live ctx for wind-down rounds (write back recognized content + summary, terminal state timeout) instead of relying on each worker/planner's `maxDur+90s` external hard ctx to kill stuck runs as `aborted_tools`. Chat rides the same harness and gains the same behavior automatically; mainagent has no `MaxDuration` and is unaffected.
- Stall backstop: when the planner wakes on heartbeat / no-change with zero open or running intents left in the whole graph, its opener switches to a stall alert stating plainly that no workers run and no queued direction exists — this round must produce one or more mutually distinct new intents (never 0). 

#### Changed

- Worker prompt refactor: intent / launch instructions / anchored-asset raw JSON move into the system prompt, rebuilt every round and never compacted away, with reruns independent of transcript-first-message retention; the launch user message slims down to just the global-posture overview (degradable, stale-tolerant). Cost: per-intent data mixed into system loses cross-intent cache reuse — a deliberate "never lose the intent" tradeoff.
- Slimmed planner / worker default bodies and fixed several field issues: planner treats `recent_done` by status (blocked/exhausted checks the trace before deciding — neither dead end nor blind rerun), negative conclusions become "observation / doubtful, not verdict" trusted only with evidence, unmet targets with no open/running intents must produce (hard floor), depth over coverage; worker negative conclusions stay "observation + tentative reading" with verdicts left to the planner, cross-intent clues go into fact summaries for the planner instead of self-chasing. Reseed appends the new defaults as new versions and switches over (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each behind a settings-flag guard); old versions stay in version history, and users who customized prompts can restore from version records.
- Converged the work-agent default toolset and asset-writeback hints: workers own single-intent execution and writeback only, while context reading / cross-work review are planning duties — so `list_facts` / `node_detail` / `list_companies` and cross-work retrieval (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`) leave its default tools, keeping only `list_findings` (dedupe before reporting) + `add_finding` / `record_fact` + `insert_assets` / `list_assets`; prompts drop stale field notes conflicting with the `insert_assets` schema (`type=tech`/`on_url`/`props`). Old DBs get a one-time migration stripping those bindings; planner/main's same-name bindings are untouched.

### Exploration graph

#### New features

- **Exploration-graph cold-node compaction (cold-digest)**: old, long-inactive intents / facts fold into digest nodes shown in `graph_overview`, with original nodes permanently kept and fully restorable by id (lossless storage, compacted presentation only, reversible folding). Hot/cold judges by reverse reachability + any-live-branch-is-hot, with R=6 round debounce and connected-component grouping; background compaction (minor folds uncovered cold blocks, major recompresses from origin merging fragments) carries liveness rechecks and cooldown exclusion — never on the hot path, never overwriting revived nodes. The overview side offers `cold_digests` plus asset-indexed `cold_index`; `expand_digest` / `expand_index` handle restore. Related / inherited tasks' overviews reuse their own folded views, and `expand_digest` supports cross-task read-only restore. `expand_digest` / `expand_index` go to planner and main agent only, not workers. All with old-DB migrations.
- `graph_overview` emits the full `finding_list`: findings are a task's highest-value output and usually few per task, so the overview now carries them all (unlike facts' recent window) — planner/worker see every confirmed finding each round without calling `list_findings`. Each trims to `{id, summary, evidence?, from_intent?, assets?}`, with affected assets as readable content (url / domain / ip:port) rather than bare ids.

#### Changed

- `graph_overview` no longer spreads `hosts` flat: the coverage block drops the host list (up to 500 host strings per round on wide tasks, of limited planning value), keeping only `host_count`; look up specific hosts via `list_assets` on demand. New top-level `done_intents_total` (finished intent count) parallels `recent_done_intents` truncated to ≤15, so the planner's dedupe knows whether anything was cut.

### Tools

#### Changed

- `add_company_scope` default binding moves from worker to planner: defining enterprise asset scope is a planning / main-control / Auto duty; workers only explore. New DBs seed the default binding as mainagent/planner/auto; old DBs get a one-time migration.
- Planner binds `list_assets` by default: now carrying both `list_assets` (DSL whole-store search) and `list_untested_assets` (in-scope untested), with old DBs backfilled once without overwriting user unbinds.

### Tasks

#### New features

- Task list gains a "Running Workers" column: counts `state='running'` intent nodes under the task, with exactly the same definition as the task-detail overview's "Running Workers" — 0 when none run.

### Network

#### New features

- New **global egress proxy** config: all target traffic can exit through one unified global proxy (http/https/socks5 with `user:pass`). With traffic capture on, it serves as the MITM recording proxy's upstream (traffic still recorded, then exits via proxy; both intercept and passthrough paths use the upstream, never leaking source IP); with capture off, it injects directly into agent bash environments and WebFetch (`proxyEnv` gains `ALL_PROXY` socks5 support). Config lives in the settings KV table (no migration needed); the frontend system-settings page gains a "Global Proxy" card, independent of the network-search proxy and LLM proxy.

### UI

#### Fixed

- Fixed wrong intent-status label semantics: `exhausted` "exhausted" → "budget exhausted" (actually cut off mid-way by step/time budgets with only partial results written back — not that the direction is explored out), `blocked` "blocked" → "execution error" (actually model / API / network failures with retries exhausted — the intent barely probed anything, not target / WAF blocking), plus the previously missing `stopped` "stopped" (user-stopped works).

### Dependencies

#### Changed

- Upgraded norma to v0.3.4: MCP tool output gains truncation plus spill-to-disk (see `651b961`), with v0.3.6 wall-clock wind-down support to follow.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### New features

- Model configs gain an "output cap" with a choice of which request field carries it: the cap limits tokens generated per single reply, sent with every request, 0 (default) = field omitted, server default decides; it is a different thing from "context window" — the latter is total model capacity, used locally only for the compaction threshold and never sent. The field-name switch matters only for `openai` (Chat Completions) format: empty (default) sends `max_tokens`, which nearly all compatible gateways accept; official OpenAI reasoning models (o-series / GPT-5) accept only `max_completion_tokens` in reverse, rejecting `max_tokens` outright with `unsupported_parameter` — such endpoints need a manual switch to the new field. The other two formats pin their own field names (Anthropic: `max_tokens`, Responses API: `max_output_tokens`), so the switch grays out for non-openai formats and clears on save. Also completes a previously never-wired chain: planner / worker / chat / Main Agent / target decomposition never set an output cap anywhere — openai-family never sent one, Anthropic rode the SDK's 8192 fallback — so adding the config field alone would never take effect; values now resolve per round like "streaming output", taking effect the round after a failover switch. Old-DB upgrades behave identically (new columns default 0 with empty field names).
- HTTP attempt status codes plus raw gateway bodies hit the server log when clicking "Test" in the background (bodies cut to 4K), easing diagnosis of 401s, quota wording, empty frames, HTML pages and the like — no more UI-folded ok / err only. This log does not depend on the "LLM records" switch.

#### Changed

- The task LLM config chain is editable in any state, no longer limited to "running / paused / chain exhausted": post-terminal (done / failed / timeout) Main-Agent chats still ride this chain, and a broken model on the chain previously could neither be edited nor continued with. Backend HTTP and DB-transaction terminal interceptions go away together; the frontend dialog opens editing and saving for terminal tasks too. Saving on terminal tasks no longer reopens quota-blocked intents (those intents would move to open with no worker to execute them and no longer qualify for "intent rerun" conditions) — continuing still goes through intent rerun / new targets, which pull the task back to running.
### Agent

#### Changed

- "Message a running Worker to redirect it" now reuses the existing pause / resume + transcript resume machinery, behaving like Main-Agent chats: the old path was a home-grown intervention persistence protocol touching scheduling barriers, recovery flows, and a dozen activity-query filters. Now messages inject through next-round input, intents run on a dedicated goroutine directly, free of the 3-slot worker pool — sent and running immediately; the frontend keeps the message box and restores the "continue directly" button, with delivery moving to SSE. No DB schema change. Tradeoff: messages live in memory only with no crash recovery, and messaging may transiently push the task one worker over concurrency (rare scenario, acceptable).

### Tasks

#### Fixed

- Fixed archiving large tasks OOM-crashing from memory exhaustion: archiving now streams snapshots to disk instead of reading whole tasks into memory; three recovery gaps on the cold-archive path are filled too — modern installs keep traffic only in SQLite, and a crash between the PostgreSQL commit and the SQLite commit used to strand archived tasks' traffic in hot storage unrecoverable — now a staging log is always written whether or not a history dir exists, and a missing `journal.json` is treated as disposable.
- Fixed system lag with many tasks: conversation loading and UI loading showed visible delays. Task list, task context, and exploration-record queries are optimized together, while the frontend dashboard, chat page, and task detail page cut duplicate requests in sync.

### Assets

#### Changed

- Dropped the "256 rules max" limit on enterprise asset scope: enterprises entering scope IP by IP / domain by domain hit this ceiling easily, and past it could only split into multiple enterprises — while scope does not get slower just for having more rules. Frontend, backend, and demo-mock caps go away together; single rules still cap at 1024 characters, and the 2 MiB request-body cap stays as backstop (≈ forty to fifty thousand rules).

### Skills

#### Fixed

- Fixed skill-bundle upload failing "upload failed: zip: unsupported compression": Go's stdlib ships only Store / Deflate decompressors, so bzip2 / Zstandard bundles written by archivers at non-default levels never unpack. Both decompressors are added (pure-Go, no new external deps); genuinely unpackable formats (Deflate64 / LZMA / XZ / PPMd etc.) and encrypted archives now fail fast before extraction with a Chinese message naming the file, the compression used, and how to repack — no more raw English errors handed to users.
- Fixed skill filename validation rejecting Chinese filenames as illegal: path validation used to be an `[A-Za-z0-9-_./]` ASCII allowlist, so one Chinese-named file (like `参考/说明.md`) failed the whole bundle as "archive contains illegal paths". Now a Unicode blocklist: filenames in any language plus spaces are allowed, while control characters, illegal UTF-8, zero-width and bidi controls (RLO filename spoofing), `\\ % # ? * : " < > |` plus `..` / absolute paths / empty segments are still refused — directory-traversal protection unchanged. Skill names open up likewise — ASCII still limited to lowercase letters, digits, hyphens (agentskills.io spec); non-ASCII letters like Chinese work directly as skill names, but spaces, dots, and path separators are not accepted.
- Fixed garbled-or-rejected filenames unpacking Chinese bundles made by Windows archivers: such zips set no UTF-8 flag and write GBK filenames — now GBK-decoded as fallback before path validation. Quoted frontmatter like `name: "中文技能"` also resolves the skill name correctly.

### UI

#### New features

- Findings page gains an "All findings" tiled view, now the default, switching with the original "group by task" view via header tabs: the grouped view needs per-task expanding to see anything, a detour when just sweeping the cross-task list. The tiled view is one big cross-task table (10/20/50/100 rows per page) behaving exactly like the grouped view — checkbox export, inline row expansion for evidence and full reports, inline rename/category/severity edits, disposition changes, deep-dives, deletes. Both views share stat cards and the filter bar; switching keeps filters, and current view plus filters persist locally; polling hits only the current view, and inline edits write through to both caches so switching never shows stale data.
- Findings page gains a "By asset" view: asset tree on the left (enterprise → root domain / IP → subdomain → service → endpoint, with the enterprise layer appearing only when assets truly have ownership), discoveries for the whole selected subtree on the right, sharing the same table and filter set as the other two views. The tree only collects assets with findings, completing ancestor chains on demand (when a finding hangs only on the deepest endpoint, upper layers still assemble), with node counts as deduplicated subtree aggregates; deleted assets or findings never linked to assets go in the "Unlinked assets" bucket. Unlike the other two views, the asset view does not poll — it queries on entering the view, changing filters, in-page finding edits, or the tree refresh button; the left tree is navigation structure with no need to recompute every 5 seconds. Each tree level shows only the increment over its parent (subdomains strip the root-domain suffix, services show `https :443`, endpoints show paths only), with full values in hover tips and breadcrumbs; oversized levels drop the endpoint/service tiers with a notice while counts still roll up.

#### Fixed

- Fixed conversation records squeezed unreadable in mobile session details: narrow viewports now collapse the session list to give width back to the records.

### Dependencies

#### Changed

- Upgraded norma v0.3.2 → v0.3.3: new `Config.MaxTokensField` lets OpenAI Chat Completions' output cap switch to `max_completion_tokens` (reasoning models only honor that key). The two keys are mutually exclusive — exactly one is sent; the default stays `max_tokens`.
- Upgraded `golang.org/x/mod` v0.37.0 → v0.40.0, fixing two dependency security advisories.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed

- Fixed the "non-streaming" switch flipping back to "streaming" on reopen after save (#69): the config-list DTO dropped the `streaming` field, so responses never returned it and the frontend fell back to default streaming on `undefined`; writes and DB storage were fine all along — only reads came back wrong. The DTO restores the field (no `omitempty` — `false` must appear in responses too).
- Connection testing now verifies the model actually replied, in the config's real send/receive mode (#65): previously only HTTP success counted, so reachable requests with zero reply (thinking burning the budget / body swallowed by safety policy / compat layer dropping `content`) still reported "connected" — disconnected from the "silent session" experience; and testing always rode streaming, so non-streaming configs tested a different channel. Empty replies now fail outright, successes show the model reply in the notice, and the current streaming switch joins the test — keeping "test passes" consistent with "session works".

### Agent

#### Changed

- `list_facts` goes paged + keyword-filtered so fact-heavy single calls stop blowing context (#74): latest 20 by default, with `limit` (cap 100) / `before` cursor / `q` summary keywords, returning `{facts, total, has_more, next_before}`; overlong single summaries truncate by characters (full text still via `node_detail`). Worker / planner prompts sync to the paged semantics. Old DBs get the new param schema brushed into the tool-catalog table via one-time migration (`SeedTool` insert-only, else the tool-manage page shows "no params").

### Tools

#### New features

- "Tool executions" page gains per-tool call counts (#72): the toolbar "Stats" button opens a dialog of per-tool call counts, shares, and failure counts (count descending); reuses the list's task / keyword filters, stats the whole result set rather than the current page, and fetches on open.

### Dependencies

#### Changed

- Upgraded norma v0.3.1 → v0.3.2: fixes silently dropped reasoning/refusal in OpenAI-family responses (Responses gains `reasoning_text`), triple-name dedupe on the reasoning field, bounded retries on empty responses (zero content blocks), and gateway 400s from compaction cutting tool pairs mid-boundary (also repairs stock orphans already baked into transcripts).

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### New features

- Each LLM config supports streaming/non-streaming switching (streaming default): on rides streaming SSE; off rides true non-streaming (`stream:false`, complete JSON in one shot), working around some gateways' miserable SSE implementations (empty frames, dropped thinking frames) at the cost of live progress and live token counts. Worker / planner / mainagent / chat / goals all resolve dynamically from the active config; the `llmpool` / `llmrec` / task-runtime three Provider wrappers all tolerate non-streaming; `llm_profiles` gains the `streaming` column with `ALTER` backfill for old DBs (default `true`, transparent to old configs).
- Support for OpenAI Responses API LLM configs: each config gains a third format `openai-responses` (hitting `POST /v1/responses`) alongside Chat Completions / Anthropic; `BaseURL` normalization, default model `gpt-5`; the `llm_profiles` `format` constraint admits `openai-responses` with idempotent old-DB migration; the frontend format dropdown gains "OpenAI (Responses API)". Depends on norma v0.3.1 (with the `reasoning_content` pass-through fix).
- Record raw LLM request/response HTTP bodies: capture the true wire body at the HTTP transport layer, preserving tool schemas, `tool_use` blocks, and raw SSE frames invisible to the normalized view; each attempt of norma's internal retries is kept; `llm_records` gains `raw_request` / `raw_response` columns with `ALTER` backfill; the records-page detail panel gains a "raw" view switch plus request/response copy buttons (`execCommand` fallback for non-secure contexts).

### Conversations

#### Changed

- Conversation-list multi-select becomes a "multi-select" mode switch: the default list no longer stations per-row checkboxes (cleaner look); the header becomes a "N total + multi-select" button; "multi-select" enters selection mode (checkboxes, select-all, bulk delete), "done" exits and clears selection, and all-successful bulk deletes auto-exit (failures stay in selection mode for retry); single rename / pin / delete stays on each row's ⋯ menu.

### UI

#### Changed

- Task management, conversation ops, and traffic viewing polish (#57): task-list column sort preferences persist, inline rename triggers straight from an icon (no menu detour), drawer (sheet) interactions plus traffic-viewer details refined.

#### Fixed

- Worker asset tags show only domains and IPs, with empty-tag cases handled correctly.

### Agent

#### New features

- Planner gains a "quantified acceptance check" when judging targets: targets carrying quantifiable acceptance criteria (asset-test coverage reaching X%, capturing N flags, obtaining some permission) must check measured values in `graph_overview` (`coverage.pct`, finding counts, etc.) before `prove_goal` — under-measured targets forbid `prove_goal` and dispatch intents to close the gap instead; "mostly done / roughly there" never qualifies a target as met early. Fixed targets demanding 100% coverage judged complete at 40% measured.

#### Changed

- Rewrote the planner prompt's basis for "0 intents this round": the original framed 0 intents as "the most common, most important principle", making the planner stand down early with targets unmet and untested surface remaining. Now 0 intents fits only two concrete cases — ① every direction considered is already covered by open/running/recent_done intents; ② next steps depend on a running work's not-yet-arrived output (wait for it to finish and the graph to update, then plan on the next wakeup). With the reverse constraint: genuinely uncovered directions independent of running works, or unmet targets with untested surface, must not stand down over "0 intents is common".
- Raised the evidence bar for "negative conclusions" in worker prompts: negative conclusions that could make the planner abandon a whole direction ("not injectable / port closed / no login entry") must exhaust the intent's reasonable means first (different encodings/params/paths/methods); thin-evidence or unfinished means always mark `confidence=inferred`, so rash `observed` negatives never weld a route shut (early-task wrong negatives especially steer off course and resist self-healing).
- The planner / worker default-prompt changes above ship as new versions appended and switched over via one-time migrations (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each behind a settings-flag guard); old versions stay in version history, and users who customized prompts can restore from version records.

### Operations

#### New features

- New `reset-password.sh` for resetting the admin (fixed username `ARTEX`) password: local / docker deployments supported, connection info explicit or auto-read from `--dsn`/`$ARTEX_PG_DSN`/`config.json`; bcrypt hashes compatible with backend login are generated in-DB via `pgcrypto` and written back to `settings.auth.password_hash`, no service restart needed. Passwords travel via environment variables, never entering process argv, with escaping against injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### New features

- Checking multiple tasks in the task list enables bulk category edits, with target categories supporting "Uncategorized" to move tasks out of their category; the whole batch writes in one transaction, so the only possible failures are tasks deleted after checking.

#### Changed

- New-task "task category" changes from dropdown to searchable input: typing filters existing categories, an unknown name creates-and-selects on Enter (or the dropdown's "Create") instantly, and the selection shows as a removable tag; still one category only.

### Conversations

#### Fixed

- Fixed messages blocked by "LLM not configured, cannot chat" when the session had an LLM config but no global active config. The pre-send check only looked at the global active config while actual execution prefers the session's config — two inconsistent resolution logics; now unified into one (session/Agent binding first, global fallback).
- Undeliverable-state hints are now subdivided by state: no configs at all hints "add a config", configs-but-inactive hints "activate one or assign one to this session" — no longer a blanket "not configured", easing diagnosis.

### Agent

#### New features

- Tasks with all targets met let the Main Agent dispatch intents via `add_intent` for Workers to pick up directly and run to a stop: the planner no longer runs (tasks with no open targets never enter planning), so it cannot re-judge targets met and cancel just-dispatched intents; the task returns to done once the frontier drains. Before dispatching, the Main Agent asks the user whether to register the intent content as a formal target based on its content; registering restores regular autonomous planning.

#### Changed

- `get_worker_trace` / `get_task_worker_trace` no longer error outright past the per-call `step_ids` cap (5): they return the first 5 steps' full content and report via `returned_step_ids` / `omitted_step_ids` / `notice` which were taken, which remain, and "no need to fetch more if this suffices". Duplicate and illegal input ids dedupe and filter before counting.

#### Fixed

- Task list now sorts by task id descending (newest first), replacing creation-time ordering. Multiple tasks created in the same instant share timestamps for unstable ordering, which — combined with 10-second polling and in-memory map random iteration — shuffled them around the list.

### Assets

#### Fixed

- Fixed company-ownership recompute killed by a single dirty row: when `assets.ip` holds a hostname (written by Agent or asset APIs), `a.ip::inet` throws `22P02`, failing and rolling back all enterprise-scope adds/edits and enterprise deletes. A safe conversion `try_inet()` now skips illegal values instead of aborting the whole statement.
- Unparseable `ip` no longer skips silently: after saving enterprise scope, an explicit notice states how many assets' `ip` is not a legal IP (with ids and values), warning that IP/CIDR rules will not own those assets; the same info goes to server logs, covering paths with no frontend response (enterprise deletes, scopesentry syncs, Agent writes).
- Task-scope IP/CIDR matching switches to `try_inet()` likewise, replacing the charset-only regex guard (all-hex-letter hostnames like `abc.def` used to slip through and trigger the same error).

#### Changed

- The `ip` field of `ip` / `service` / `endpoint` assets no longer accepts hostnames: `insert_assets` and asset APIs return per-item errors with `index` plus the exact remedy (switch to `type=subdomain` with `domain`, or resolve A/AAAA records first), which Agents can follow to fix and reinsert. Other assets in the same batch are unaffected and store normally.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### New features

- Running Workers support individual pause, resume, and cancel; pausing keeps intents, facts, and findings, while canceling transactionally cleans the current intent plus its direct output after execution exits.
- New `paused` intent state, execution fences, and named termination reasons prevent late blackboard writes after pause, cancel, or task deletion.
- Optional task concurrency cap added; new, restored, finding-deepened, and queue-backfilled admissions share one persisted FIFO admission path.

#### Changed

- Worker single-run wall-clock default moves from 600s to 1200s; timeout-revived tasks re-time on the next real execution.
- Worker, Planner, and Main-Agent real execution states uniformly drive task status badges — fixing "idle" display while Workers run.
- Worker sessions keep individual controls plus current-session token totals, with model names moved to the right of the current session title.

#### Removed

- Removed Worker multi-select, select-all, and bulk pause/resume UI, along with Worker bulk-control APIs and mock contracts.
- Removed per-row Worker-list token badges and tooltips; the underlying full token ledger and task-aggregate APIs stay.

### LLM

#### New features

- Tasks support an ordered LLM config chain; clearly recognized provider-quota exhaustion auto-switches to the next config, persisting current config, exhaustion state, and error summaries.
- Running and paused tasks support editing the full config chain, reordering, and manual current-config switches, effective from the next LLM call.
- Auto-switches, manual switches, and full-chain exhaustion write structured system activities with deduped popups through the task activity stream.
- New task-role model resolution API resolving the config and model for the Main Agent, Planner, and Worker's next call in one place.
- Optional global LLM Pool supporting call ordering, per-model failure fallback, health states, cooldown recovery, and manual reset.
- Always-on LLM usage ledger aggregating input, output, and cached tokens by task, session, model, and config.

#### Changed

- Target decomposition, Planner, Worker, and Main Agent share the task-level LLM runtime (precedence below).
- Failover answers only to explicit quota, balance, or billing errors; ordinary throttling, auth, network, server, and context errors never switch wrongly.
- LLM settings page becomes a config-card list with right-side drawer editing, plus pool rotation order, priorities, exclusions, health states, and recovery actions.
- Chain-exhaustion info wraps on narrow screens; the current model moves from a session-list icon to a text label beside the current session title with a full tooltip.
- Task role LLM resolution order becomes "Agent binding → task chain → global": explicitly model-bound roles always run on that model; unbound roles fall to the task chain, then to global when the task chain is empty.
- Empty outbound proxy means direct — no longer falling back to `HTTP_PROXY`/`HTTPS_PROXY` env vars (explicit `ARTEX_LLM_PROXY` unaffected); proxy input accepts `socks5://user:pass@host:port` with credentials.

#### Fixed

- Transient pre-output streaming failures retry with safe backoff on the same provider, visibly reducing "ran one or two tools then broke, no summary, terminal `model_error`" cases; quota exhaustion, overlong context, and deterministic 4xx do not retry — failover and compaction recovery handle those respectively.

#### Removed

- Removed the session-list LLM icon marker to avoid confusion with Worker status icons.

### UI

#### New features

- Tasks support global category create, rename, delete, and filter; new tasks can pick a category directly.
- Global task-template CRUD plus right-side management drawer; new tasks can load preset descriptions and targets, or save current content as a template.
- New tasks can link multiple source tasks and multiple enterprise asset scopes; enterprise scope auto-recognizes domains, URLs, IPs, CIDRs, ICP filings, and enterprise keywords from a single multi-line textbox.
- Task test assets support live add and delete, recording human, enterprise, inherited, or Agent-discovered sources; Worker session titles show current test assets plus a source summary beside them.
- Findings page groups by task with independent in-group paging, supports describing findings and creating high-priority deep-dive intents.
- Conversations support rename, pin, unpin, and delete; task list supports current-page bulk pause and resume.
- Traffic detail and tool-execution detail move to right-side drawers; HTTP messages complete `Host` and highlight request lines, status codes, headers, JSON, and markup bodies.
- Task-list action column gains "Details" and "Pause/Resume" buttons; session send-key combos are configurable in system settings (`Enter` / `Cmd+Enter` etc.), and the web-search area gains a proxy input.
- Session-title right badges show the LLM config name, with model IDs on hover tooltips; tasks can set an optional name, with the task list gaining a name column plus search (falling back to description when empty).
- Skills page gains usage counts, last-used times, and missing-dependency stats to pinpoint dead or uninstalled skills.

#### Changed

- Task titles become focusable detail links; task assets, enterprise assets, finding task-groups, and in-group findings use real server-side paging, stable sorting, and exact totals.
- New enterprises use a right-side drawer; scope entry unifies on multi-line text with live recognition, validation, and type previews.
- Main-Agent input supports autogrow, `Enter` to send, `Shift+Enter` for newlines, and avoids mis-sends during CJK IME composition.
- Agent previews, task reports, and related details share one Markdown renderer; delete confirmations, long errors, and mobile drawer widths are fixed together.
- Task template picker displays and searches by template name but submits template IDs; system font sizes restored to original, app version uniformly shown as `0.3.3`.
- Task totals and current-session tokens highlight only input, cache-read, and output numbers; the send button uses a cleaner up-arrow icon.
- Task overview test scope shows enterprise names rather than IDs.

#### Fixed

- Plain text with dots no longer misdetects as ICP filing numbers.
- Fixed variable-catalog collisions with global runtime variables (like `{{.Now}}`) rendering duplicate keys in the Agent editor variable list.

#### Removed

- Removed the task-card standalone "Enter" button — clicking the task title enters details everywhere.
- Removed the dashboard "New task" button, new-enterprise Logo URL input, and finding-count badges atop findings and in task groups.
- Reverted the global 10% font-size increase, and removed session-list Worker checkboxes, model icons, and row-level token stats.

### Agent

#### New features

- New tasks can link multiple existing tasks, live read-inheriting directly-sourced targets, facts, findings, completed intents, asset scopes, and blackboard context. 
- Blackboard read tools support on-demand queries of source-task nodes, facts, findings, and execution traces; inherited nodes carry source marks and all write tools refuse modification.
- Enterprise scope tools support domains, URLs, IPs, CIDRs, ICP filings, and enterprise keywords; keywords serve only as Agent scope hints, never participating in automatic asset ownership.
- Finding deep-dives create high-priority human Worker intents in the original task with asset anchors and `derived_from` edges.
- Overview gains a "Target Management" card with human target view/add/edit/delete; adds or edits notify the planner and revive the task, deletes hard-delete target nodes (cascading edges and anchors) without reviving.
- Main Agent gains the `steer_work` tool by default for live corrective injection into running Workers without interruption or progress loss (still validating intent ownership by task).

#### Changed

- Source-task intents never enter the new task frontier; new tasks keep independent exploration, execution queues, workdirs, and conversation history.
- Deleting a running intent no longer destroys data: it stops to `stopped`, requires a deletion reason, hangs the reason as a fact on the intent plus payload, and the planner senses the `cancelled` trigger (intent content and reason kept).
- Main-Agent conversations switch to server-driven activity streams, with failed sends recovering input; opening a session auto-pins to bottom and keeps the last reply visible after detail lazy-loads.
- Task pausing terminates current Main-Agent, Planner, and Worker calls but does not stop users from launching new Main-Agent orchestration sessions mid-pause.
- Cancel, close, and stream interrupts uniformly keep the true termination reason, generated content, round counts, elapsed time, tokens, and unreturned tool calls.

#### Removed

- Removed Main-Agent client optimistic message echo to avoid duplicate messages and cross-session streaming under pause, failure, or concurrent activity.

### Constraints

#### New features

- Target decomposition extracts operation constraints (allow/deny) before splitting targets, with Main-Agent runtime additions; constraints inject into Planner and Worker system prompts at top priority, with debiased diversity and surface-spreading exploration explicitly obeying constraints.
- Overview gains a "Constraint Management" card with CRUD APIs; constraint injection scope toggles per Planner / Worker (both on by default, read every round, effective immediately). New table `task_constraints` uses `CREATE TABLE IF NOT EXISTS`; old DBs auto-create it.

### Interception

#### New features

- Command interception gains model-fallback approval beyond regex/string rules: with zero rule hits, the model renders an `ALLOW`/`ASK`/`DENY` semantic verdict, with failure-fallback and approval-timeout actions configurable; the interception page splits into "Rules / Model config" tabs, and model verdicts carry the `[模型]` prefix plus reason.

#### Fixed

- Hardened verdict-output parsing so correct `DENY` verdicts are not mistaken as allows.

### Traffic

#### Changed

- Traffic recording moves to whole-exchange SQLite rows (big bodies spill to hash-deduped blob buckets), text bodies gain trigram full-text indexes supporting arbitrary-substring and Chinese search; deletion degrades to a single-SQL-statement transaction, dropping from hours to milliseconds with recording never paused. New big-body streaming downloads plus `traffic_search` full-text params; legacy file-tree data needs no migration — still readable, searchable, deletable.

### Tasks and assets

#### New features

- New tasks gain an "asset coverage" feature switch (default on): off means no coverage computed or shown, posture graph shows assets only, `task_scope` stops auto-accumulating, and related tools leave Planner / Main Agent; enterprise links ignore this switch.
- Per-asset `related` flag on `insert_assets` (default `true`): effective only with coverage on; `false` enters the shared asset store only, excluded from the task's coverage (e.g. incidentally found neighbor sites or irrelevant assets).
- Enterprise scope and task test assets uniformly recognize domain, URL, IP, CIDR, ICP, and keyword text; domains/IPs can create or reuse global assets, while CIDR/ICP/keywords stay as task-scope context.

### Build

- `build.sh --release` builds Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 in one shot, producing zip bundles with `skills/`, config examples, and README.
- Go linker strips debug info for zip bundles; UPX becomes explicitly opt-in so its self-extracting ELF stops segfaulting at startup on some Linux environments.
- The Release Workflow smoke-tests the Linux amd64 binary at boot and ships `SHA256SUMS` with bundles.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks can link multiple existing tasks, live read-inheriting directly-sourced facts, findings, completed intents, asset scopes, and blackboard context; new tasks still use independent execution queues, workdirs, and session history.
- Tasks support an ordered LLM config chain. Clearly recognized provider-quota exhaustion auto-switches to the next config, persisting current config, exhaustion state, and structured audit activities.
- Running and paused tasks support editing the LLM config chain, reordering, and manual current-config switches; auto-switches, manual switches, and full-chain exhaustion surface on the task detail page.
- Running Workers support pause, resume, and cancel. Pausing keeps blackboard data; canceling transactionally cleans the intent plus its directly produced facts, findings, and execution records after the Worker stops writing.
- Deleting a task can optionally clean linked assets, traffic, findings, and task workdir files together, with a deletion barrier, concurrency guards, and auditable deletion stats.
- Optional task concurrency cap added; over-cap new tasks queue FIFO and auto-start when a run slot frees.
- Task-asset paging, company-asset paging, task-category stats, and paged-intent mock contracts added.
- New `build.sh` single-binary build script supporting static frontend export, asset embedding, cross-platform targets, and build-version injection.

### Changed

- Task-level explicit LLM chains coexist with the existing global LLM Pool; explicit chains keep strict quota-failover semantics, while chainless setups follow Agent-binding and global-config rules.
- Main-Agent input supports autogrowing multi-line input with `Enter` to send, `Shift+Enter` for newlines, avoiding mis-sends during CJK IME composition.
- Planner, Worker, and Main Agent share the task-level LLM runtime; the task chain uses the smallest context window among candidate models as the safe compaction threshold.
- Task detail pages show running, idle, and paused states from real LLM call states; source-task facts, findings, intents, asset references, and graph nodes uniformly carry source marks and stay read-only.
- Agent previews, task reports, and related detail views share one Markdown renderer.
- LLM model configs use card-and-drawer interaction, with independently scrolling model lists inside drawers.
- Task pausing no longer blocks Main-Agent chats: Main-Agent orchestration sessions are independent of task pausing — new messages still send mid-pause (pausing only terminates the round in flight).
- Worker single-run wall-clock default moves from 600s to 1200s.

### Fixed

- Removed Main-Agent console optimistic echo for pure server-side rendering, fixing garbled messages and cross-session content under pause or send-failure scenarios.
- Opening a Main-Agent session scrolls to bottom by default with the last reply fully shown: lazy-loaded last replies auto-pin to bottom after expanding, never pushed off-screen.
- Fixed paused-task Main-Agent sessions that kept running, and orchestration Agents pausing tasks without stopping the Main Agent alongside.
- Fixed task-status badges and action buttons desynced from real Planner, Worker, or Main-Agent runtime states on task completion.
- Fixed potential late blackboard writes or leftover files between Worker cancel, task deletion, and concurrent writes.
- Fixed dead Markdown previews, long-text delete-confirmation overflow, mobile widths, and misaligned task-detail buttons.
- Named termination reasons on all Agent cancel paths, with canceler, terminal state, round counts, elapsed time, token usage, and unreturned tool calls visible in activity details.
- Fixed a race where the parent context could preempt the named `shutdown` reason at backend shutdown, plus garbled truncation from byte-slicing Chinese activity summaries.
- Fixed canceled events with partial streaming output losing the true termination reason, while keeping pre-cancel generated content.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
