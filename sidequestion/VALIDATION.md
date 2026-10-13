# `/btw` Verification Record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

Uses a standalone PostgreSQL test database and data directory; live model credentials were injected into the standalone test environment only — never written into code or this record — and the product default model was not modified. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

Live model conversations, returned objects, engineering assertions, and the Qwen raw review text are saved in [validation-2026-09-10.json](validation-2026-09-10.json), which contains no API credentials.

## Engineering Checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Structured messages, tool-arg deep copy | Pass | `TestCheckpointDeepCopyAndBoundaries` |
| Summary/compaction requests don't overwrite, full replies and terminal states publish, partial replies excluded | Pass | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Actual model-pool member identity | Pass | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-group replay, budget trimming and over-limit errors | Pass | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main/side parallelism, two-way cancellation isolation | Pass | Blocking provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, streaming/non-streaming, usage on failure | Pass | `TestServiceNoToolsAndUsageOnFailure` |
| Real norma ChatAgent + local Read tool, main transcript/activity isolation | Pass | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming subcases |
| Persistence, paging, idempotency, partial-answer retention across restarts | Pass | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear vs late-write races, parent deletion, version comparison | Pass | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent/Worker archive and restore, v1/v2/v3 | Pass | `TestSideTaskArchiveVersions` |
| Three parent interfaces, auth, resource ownership, Worker logical deletion | Pass | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Busy main session still takes side questions, standalone SSE reconnect/disconnect, cancel, clear | Pass | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 1 per parent / 4 global concurrency | Pass | Two `TestSideHTTP…` cases |
| Snapshot persisted before submit, resume after restart, old sessions can't forge snapshots | Pass | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Reject after cached config deleted or model changed | Pass | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancel and wait for final answer + usage persistence before archive | Pass | `TestSideTaskDrainPersistsBeforeArchive` |
| Usage recorded exactly once on early consumer cancel, with side attribution | Pass | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Auto-recovered Worker/deadline run contexts keep publishing new snapshots after restart | Pass | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Race checks on related packages | Pass | Commands below |
| TypeScript and production build | Pass | `npx tsc --noEmit`, `npm run build` |
| Biome on new frontend modules | Pass | `biome check`, 3 new modules |

To reproduce the automated checks, point `ARTEX_PG_DSN` at a separate disposable database (never at production):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

The full Go regression is not all green: two pre-existing `server`-package tests fail at temp-dir cleanup, both reporting `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

Re-running the `server` package from baseline sources (unmodified) in the same isolated environment reproduces both cleanup failures. The baseline run also showed a `TestCoreTaskLifecyclePG` target-node-count assertion failure; the final modified `server` regression does not have that assertion failure. Other packages pass, and this round's side-related cases plus race checks pass. Baseline issues are not marked as accepted here, and existing assertions were not edited to hide issues.

The Next.js build output has the usual multi-lockfile / workspace-root inference warnings; the build completes and all pages generate successfully.

## Browser Checks

Used the Codex in-app browser against a standalone local Go service and Next.js dev server. Desktop and a narrow 390 × 844 viewport completed the following manual-automated operations, checking screenshots and browser logs:

- Type `/btw` while a plain chat run is in flight; main content and side answer show together; desktop sidebar is fine.
- Follow-up questions in sequence; stopping the side request keeps the generated part; the main flow continues.
- The request continues when the panel closes and the finished answer is restored on reopen; an empty `/btw` restores history after a page refresh.
- Narrow-viewport Drawer input, buttons, history, and close all work with no horizontal overflow.
- Clearing uses a confirmation dialog; history is gone afterwards while the main transcript and snapshot stay.
- Task MainAgent and two Workers take questions separately and switch without cross-talk between agent labels and histories.
- A blocking local-model fixture keeps the Worker running; submitting `/btw` from the Worker's main input box, then stopping the side request, leaves the Worker showing live running state with its own pause button, and the side request keeps its partial answer.
- Browser error/warning logs are empty.

Controllable fixtures verify concurrency timing precisely without depending on live-model output speed. Two Worker runtime checks during debugging never formed a valid concurrency window (task already finished / answer ended early); they were redone with a fixed fixture and passed, and those initial runs are not counted as valid passes.

## Live Model Conversations

Probed `grok-4.6` first via the OpenAI-compatible endpoint `http://127.0.0.1:12580/tingly/openai`. Probe returned HTTP 200 with model name `grok-4.6` and `READY` in 2.82 seconds. It was the preferred available option, so the Tingly `glm` and Zhipu `glm-5.3` fallback chains were not enabled; those two fallback services were not verified this round.

| Scenario | Actual result |
| --- | --- |
| Ask about assets, targets, markers while the main session runs | Returned `redhaze.top`, homepage read and target summary, `BTW-REAL-0910`; side request completed in 16.97 s |
| Ask for tool evidence after the main session finishes the homepage read | Correctly cited WebFetch 200, curl redirects 301 → 302 → 200, page title; 7.24 s |
| Side request asks Bash to create a test file | Refused to execute; target file not created; 7.74 s |
| Side request after completion leaves main context unchanged | Main transcript SHA-256 and main activity records unchanged; side tool execution count 0 |
| Ask again after a real Go-service stop/restart | Kept the previous 3 side history items and answered about assets, markers, and title straight from the persisted snapshot without re-running the main Agent |
| New session with the Grok non-streaming config | Correctly answered assets and `ATOMIC-0910`; usage returned and saved: input 11734, output 138, cache_read 11520 |

The asset case's main session used WebFetch and Bash/curl to read the public homepage, landing on `https://id.redhaze.top/home` with the title "红幕科技 RedHaze Group · 全球综合集团门户" ("RedHaze Technology RedHaze Group · Global Conglomerate Portal"). Bash staged the response in a local test file; nothing was written to any remote. That fact is verified separately from "the side request executed no tools".

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage caveat:** Tingly's Grok streaming responses return no usage. A separate direct send with `stream_options.include_usage=true` verified this: HTTP 200, 12 data frames, 0 usage frames. So the 0 in streaming tests means the endpoint provided no usage — not that there was no billing. Non-streaming usage and fixture failure/cancel usage are all saved correctly.

## Qwen Review

Review model `qwen-flash` via the OpenAI-compatible endpoint `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. It received the first three live side conversations, main-session tool evidence, and engineering assertions; it returned `verdict: accept`, `concerns: []`, judging the answers consistent with the assets, markers, and page-read evidence and the side tool refusal within constraints. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review did not cover the later-added service-restart and non-streaming tests. Qwen's "no writes" summary was too broad: the main session's curl did create a local response temp file, as recorded above. Concurrency, zero tool execution, and transcript isolation are judged by engineering assertions; the model review only assists answer-quality assessment.
