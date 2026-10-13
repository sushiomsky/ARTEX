# Side-Conversation Context Budget

Fixed 2026-09-11. The original implementation treated the request JSON's character count directly as tokens and inherited the main task's 32K output reservation, so side questions were rejected prematurely when HTML, JS, and tool results grew large.

## Open-Source Implementation Cross-Check

- [Grok CLI side context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): extracts snippets from recent user/assistant text with a ~2000-character budget, at most 400 characters per message. No continuous side-Q&A history is maintained on this path.
- [Grok CLI standalone request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): standalone cancel signal; output cap of 2048 tokens when the model supports it; no tools.
- [Grok CLI main-session compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): estimates tokens, keeps recent content, merges new content into the old summary, and handles cross-turn truncation.
- [OpenCode session compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): full request estimation, output/buffer reservation, recent content plus rolling summary, tool-free summary requests; that implementation defaults to an 8000 recent budget with a 4096-token summary output cap.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): overflow recovery is attempted only when no assistant output has started; the retried call does not re-enter the same overflow-recovery path.

ARTEX borrows the standalone output budget, recent-content-plus-rolling-summary, and bounded-recovery approach. It keeps norma v0.3.6's structured messages with tool pairing, does not copy Grok's text excerpting, and does not write OpenCode's main-session compaction events into the ARTEX main transcript.

## Request Budget and Execution

- Messages reuse norma's per-content-block UTF-8 byte estimation with a 4/3 margin; system prompt, tool schemas, and message-envelope overhead are counted additionally. The estimate is not an exact model token count.
- Side output defaults to at most 8192 tokens and never exceeds the main config's output cap. The service env var `ARTEX_BTW_MAX_OUTPUT_TOKENS` sets a cap of 256–32768; it never changes the product default model or main-task parameters.
- The input budget is the context window minus the output cap and a safety margin; unknown windows use the platform default of 200K. The safety margin is 5% of the window, minimum 128 and maximum 8192 tokens.
- Successful Q&A loads at most 20 groups per batch by ascending ordinal. At most 20 groups of original text are kept, with a token budget of at most 1/4 of the input budget and no more than 16K.
- Over-budget Q&A is folded into the rolling summary. The summary carries history provenance and context timestamps; historical assistant answers are not new tool evidence, and on conflict the latest main snapshot wins.
- When the main context is still too long, only old messages in the summary copy are condensed, keeping at most 8K tokens of recent content; cut points never split a tool call from its result. An oversized single group enters the summary whole.
- Summary input is chunked UTF-8-safely against the actual remaining window, with a 2048-token output cap; empty summaries, truncation, tool calls, or over-budget summaries are never cached. A single side request makes at most 12 summary calls under the same 120-second timeout; hitting the cap fails explicitly instead of looping forever.
- If the model returns context-overflow on the first call with no text or tool-call output yet, retry at most once after further shrinking; if the estimated size did not drop, stop recovery immediately. Other model errors and partial streaming output do not trigger this recovery.
- All obtained usage — including summaries, failed attempts, and usage at cancellation — accumulates onto the same side request. When the provider returns no usage, only zero may be recorded; estimates must not be disguised as actual usage.

## Persistence and UI

`side_question_sessions.memory` stores old Q&A summaries, covered ordinals, and main-context summaries cached by snapshot identity. `side_question_requests.context_info` stores the preparation phase, actual replayed group count, summary usage, and budget estimates.

Summaries are saved only while the original request is still running and the cleanup version matches; clearing wipes the cache too, and late writes never restore cleaned data. A new snapshot never reuses an old snapshot's summary. Summary fields persist with v3 task archives; restoring old v3 archives missing the fields backfills empty objects, and v1/v2 stay compatible.

POST admits the request and returns it first; preparation and compaction run in the background without holding the admission lock or a database transaction. SSE/history shows the prepare, Q&A-organizing, copy-compaction, and answering phases; a compaction failure is saved as that side request's failed terminal state. The frontend keeps the error and restores the failed question's draft instead of covering the input with a floating notification; history polling no longer clears submission errors.

## Verification Record

- 19/20/21/50-group replay, old-conclusion retention across 20 groups, summary-cache reuse across restarts: automated pass.
- Overlong Chinese answers and code context, chunked request budgets, tool pairing, unchanged snapshots, new-snapshot cache invalidation: automated pass.
- Summary failure/cancel/truncation/overlong/tool-return, clear races, call caps, single overflow recovery, and no retry on partial streams: automated pass.
- Standalone PostgreSQL paging, restarts, v1/v2/v3 archives, summary-cache and budget-metadata archive restore, old v3 missing new fields, 20 parent sessions sharing four concurrency slots: pass.
- Go candidate-service build, frontend TypeScript check, Biome check on changed components, and a Next.js production build in a standalone directory: pass.
- The built-in browser used a standalone UI fixture to verify the organizing phase, summary-range hints, draft recovery after failure, no floating error notifications, no horizontal overflow, and a clean console at 1280×720 and 390×844. The temporary fixture has been removed.
- Read-only replay of an existing local Worker snapshot with the new budget check passes; e.g. Worker #3's 293085-character snapshot is no longer wrongly rejected by the local character count. No external model calls for this item.
- Sending a private Worker snapshot to Grok's live chat for this round was rejected by automated approval review, so it never ran and does not count as a pass.
- 2026-09-11 00:37 restarted the local backend at the user's request, reusing the original database, data directory, and login config. The running binary matches the candidate binary SHA-256; `/api/health` on both the backend and the frontend proxy returns healthy.

Verification commands (test database only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build uses a standalone copy to avoid overwriting the current preview's `.next`. The candidate service lives at `/private/tmp/artex-btw-budget-candidate`, copied to `/private/tmp/artex-btw-preview/artex` and started there; the original binary is backed up as `artex.before-context-budget` in the same directory.
