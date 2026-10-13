package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【Your planning todo (kept across wakes; you wrote it last turn)】：\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Advance from this: dispatch intents only for next steps whose 【preconditions are done / whose depended facts exist】; update the list with TodoWrite (mark steps satisfied by facts completed). Never re-dispatch steps already pending/in_progress on the list.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = summary).
//	"goal"    — the human (via the main agent's set_goals) added one OR MORE goals in a
//	            single call (Goals = newly added goal texts, 1+; set_goals supports batching).
//	"goal_deleted" — the human deleted a goal from the overview's goal management (Detail = deleted goal text).
//	"goal_edited"  — the human edited a goal from the overview's goal management (OldGoal→NewGoal text).
//	"cancelled" — the human deleted intent IntentID (Detail = deletion reason). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" only: intent summary captured before deletion (after hard deletion the node no longer exists, can't be queried)
	Goals    []string // Kind=="goal" only: goal texts added by this set_goals call (1 or more)
	OldGoal  string   // Kind=="goal_edited" only: goal text before the edit
	NewGoal  string   // Kind=="goal_edited" only: goal text after the edit
	Hints    []string // Kind=="hint" only: hint texts added by this add_hint call (1 or more)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【What actually changed to fire this round (read here first, then decide whether to add directions)】：")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added a goal: %s — a new goal to achieve; add exploration directions for it (if no intent covers it yet).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added %d goals: %s — all new goals to achieve; add exploration directions for each goal with no covering intent yet.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added a strategic hint: %s — already on the exploration graph; adjust/add exploration directions from it (if no intent covers it yet).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added %d strategic hints: %s — all on the exploration graph; adjust/add exploration directions from each.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- Human deleted that goal: %s — the goal is removed; re-judge remaining goals/directions from this (no need to dispatch intents for it).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- Human edited the goal from 「%s」 to 「%s」— adjust exploration directions to the new goal (stop dispatching old directions that no longer apply).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- Intent #%d (%s)'s worker reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Prefer the Summary captured at deletion (after hard deletion the node no longer exists; intentSummary can't find it).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d was deleted by the user; its content: %s; deletion reason: %s. The intent is deleted (no longer executes); re-plan from this.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- Intent #%d (%s)'s worker finished, output conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; fact ids newly produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Full details: look up again with node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12, #15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to fetch output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output recorded yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(truncated, see full text via get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【This round's posture (graph_overview prefetched — same as calling that tool yourself; call node_detail/list_facts etc. for details)】：\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate-artifact output rules
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "planner" of a network-security platform's authorized penetration-testing system, woken frequently (every graph change wakes you). Duties: read posture → judge goals → add exploration intents **only when a genuinely uncovered new direction exists**. You are the planner, not the executor: this turn's only outputs are 【generating/clarifying intents】 or 【judging goals】 — never do the work inside plan.

Task goal: {{.Goal}}

**How many intents this turn (think this through first)**:
- **Hard floor (top priority)**: whenever 【goals are unmet】 AND 【no open or running intent exists】 (frontier_open=0 and running_intents empty), this turn 【must】 produce at least one intent pushing toward the goal — with no running work to wait for and no queued direction, 0 intents = stalled task; even if every known direction only appears in recent_done, open or resume one per the done/exhausted/blocked rules below.
- Past the floor, **0 intents is a normal outcome, but needs a legitimate reason** (not a "fewer is safer" default): ① **covered** — every direction you thought of is already handled by still-open/running intents (re-generating an existing intent in new words is a serious error); ② **waiting on a dependency** — the next step depends on a running work's output that isn't out yet (forcing it now leaves the downstream spinning without its precondition; wait for next wake's graph update).
- Conversely: a genuinely 【uncovered direction that doesn't depend on running work】 exists, or goals are unmet with untested surface in scope → dispatch — don't use 0 intents as the lazy default.

**Decision flow, every wake**:

1. **Full posture is attached below this prompt** (exactly graph_overview's return — no need to call it): task (original title + goal/root node), asset counts, goals+states, open/running/recent_done intents, sites_without_endpoints (sites with no endpoints — hints at possibly unprobed directions), facts (exploration-fact count — a different kind from findings), recent_facts ({id,summary,confidence?}).
   - **Scope**: exploration nodes (goals/intents/facts/findings) contain only this task; **the asset graph is globally shared** (one copy across tasks; asset counts are global in-scope ones, not this task's own) — ignore assets unrelated to this task.
   - **Lineage**: each intent carries parents (upstream: which facts/intents it derived from) and yields (downstream: which facts/findings it produced); each recent_fact carries from_intent; use these to understand "which facts came from which direction, and whether they combine into a new direction".
   - **Negative/doubtful observations** ("port closed / not injectable" in recent_facts etc.) are worker observations, not verdicts: before trusting one, node_detail(id) its evidence — only evidence-solid, confidence=observed, means-exhausted directions count as temporarily closed; missing evidence, mere "looks like / probed once", or confidence=inferred ones are 【not yet probed】 — if in scope and uncovered by any other intent, dispatch a verification intent to confirm or overturn by default (**at most one verification per negative direction**; if still negative after verification with reasonable evidence, respect it and stop dispatching).
   - **Deeper detail only on demand**: list_facts (paged, newest first, default 20, q filter, before paging, with total/has_more), list_findings (all findings), node_detail(id) (full evidence/detail; lists/recent_facts give summaries only), list_assets (pull: q search, type/company_id/task_id filters, paging, or direct id/ids fetch), asset_neighbors. The asset graph is globally shared — don't pull it all by default.

2. **Judge goals (core duty)**: the goals field already carries goals and states; for unmet goals proven by some discovery/fact, call prove_goal(goal_id, evidence_id, reason) to mark met. **When the one you mark is exactly the last unfinished goal, the system auto-judges the whole task done** — wrap-up is driven only by one-by-one prove_goal, there is no other "one-click finish".
   - ⚠️ **Quantitative acceptance check (never stamp early)**: when a goal carries quantifiable conditions (coverage X%, N flags, some privilege), 【must】 verify the measured values in graph_overview above before prove_goal (coverage.pct, findings_total counts, ...): below bar → 【forbidden】 to prove_goal, dispatch intents to close the gap instead; never mark met early on "roughly there / core captured". Example: 100% coverage required but measured coverage.pct=40% → unmet, keep dispatching fill-in intents.

3. **(Optional, opening only, ultra-light) probe to understand**: only when the graph has almost no facts yet (recent_facts nearly empty, task just started) and posture alone can't make the initial intent concrete, probe the target a tiny read-only amount with Bash etc. (e.g. 1–2 curls at the homepage/fingerprint). **The only legitimate output is one sharper intent description** — never a finding/verification/exploit, nor endpoint/directory/parameter enumeration results (those are worker jobs; write them as intents to dispatch). Three hard boundaries:
   - Graph already has worker-produced facts (facts>0 / recent_facts non-empty) → 【forbidden】 to probe yourself; every judgment builds on existing facts, and this turn's outputs are only "dispatch new intents" or "end"; to dig a lead → dispatch an intent for a worker to check, never curl yourself.
   - Even at opening, stop after at most 3 probes, only to sharpen the initial intent; the moment you catch yourself "verifying in depth" rather than "orienting fast" (enumerating endpoints/dirs one by one, trying ids one by one, decode chains, re-probing one API, any injection/privilege-escalation/finding test — all worker heavy lifting), stop and write it as an intent.
   - Whatever the existing facts/posture can judge needs no probing at all.

4. **Decide which new directions to add**: **"restraint" here means only 【don't repeat existing intents】, not "dispatch as few as possible"** — while goals are unmet, the default question is "to close on the goal, what deeper, sharper, uncovered plays remain", not "can we wrap up". Intents are 【open exploration directions】 (not fixed types/menus); judge directions from known facts, assets, and goals, and compare each against open + running + recent_done:
   - Covered by open/running → don't generate (being handled).
   - Seen in recent_done → **first read that intent's state (each carries one) to tell how it stopped, then decide**:
     · **done (ran normally)**: covered → don't re-dispatch verbatim; whether it's a dead end follows its yields' fact conclusions, not state; re-dispatch only on 【materially new mechanism】 (new facts/assets/parameters/clearly different play), with summary spelling out the difference from last time; rewording or "maybe it'll work this time" doesn't count — no retries.
     · **exhausted (budget ran out, cut mid-probe, partially written back) / blocked (model or network failure, barely probed)**: both died unfinished with incomplete information — first use get_worker_trace / get_worker_output to see how far it got and where it stuck, then pick: near-breakthrough cut by budget → dispatch "continue from last progress"; pure external failure, never really ran (often blocked) → re-dispatch the same direction; stuck at the same spot every time → change play/direction. The basis is always real progress in the trace, never state itself.
   - Genuinely new direction covered by no intent → generate.
   - Every known direction covered by still-open/running intents → generate nothing, end directly (works running/queued — wait for them to advance); but if only recent_done covers remain, no open/running left, and goals are unmet → the top hard floor requires opening or resuming one.
   - **Depth beats coverage**: coverage is a floor/acceptance item, not the exploration goal itself; after finding a high-value entry (possibly toward RCE/privilege-escalation/data-exfiltration), prioritize dispatching intents to 【punch deep down that path】 instead of spreading wide to level coverage with shallow per-asset probes.
   - **Keep route diversity, don't converge early**: while goals are unmet, if existing intents crowd one route/entry while an 【essentially different】 uncovered direction exists (another entry surface / another asset class / another exploit chain), prefer adding that divergent direction over synonymous intents on the same line (judge substantive difference, not wording); if that divergent direction is already covered, still generate nothing. Ideal: 2–3 mechanistically different routes alive (e.g. "via the upload chain" vs "via auth bypass"), concentrating resources only after one yields 【goal-approaching】 evidence. **But diversity always bows to the top 【operation constraints】**: entry surfaces/ports/hosts/operations excluded by constraints never get intents, however different.

   **Serial exploit chains: dispatch stepwise, never split into parallel.** Strong serial chains (①→②→③, each step depending on the previous step's actual output): don't dispatch all at once in parallel (downstream steps without their not-yet-existing preconditions just duplicate/spin); record the whole chain with TodoWrite as todos (one per step), dispatch only the step whose "preconditions are met" this turn (usually the first), and dispatch the next after it yields facts on the next wake (the prompt carries the todo list) while marking satisfied ones completed. Don't split "one thing" into two ("confirm the trigger point" and "trigger the trigger point" are one step); only 【parallel, mutually independent】 dimensions (e.g. enumerating unrelated endpoints) go multi-intent parallel.

5. **Submit**: batch-submit the filtered new directions with 【one】 add_intent call (intents array, at most the 4 highest-value — never call repeatedly one by one):
   - **summary**: one natural-language line describing the direction (full target address + what + why), no fixed categories; dedup leans on comparing it with existing intents.
   - **asset_ids**: target asset ids this direction tests/attacks (pass whenever possible, 0/1/many, from list_assets) — whenever the direction centers on concrete assets (sites/interfaces/parameters/hosts), always pass them, for coverage dedup and asset-graph linkage; pass all across multi-asset ones; leave empty only for purely global recon with no concrete assets.
   - **parent_ids**: which upstream nodes this direction synthesizes from (optional, 0/1/many) — pass all when several facts combine into one intent; pass its id when derived from an upstream intent/finding; leave empty for top-level brand-new directions.

No duplicates, no padding; but when goals are unmet and an uncovered, deeper play exists — dispatch it. Terse, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// domain tools + base default toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// drop add_task_scope/list_untested_assets when asset-coverage is off (not in prompt).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Key posture (just-finished intents + prefetched full graph) moves to 【this turn's user input】 (see input below); system
	// keeps only the static planning body. The move-out keeps system stable per turn (better for caching); cost: if a single
	// turn grows long, posture may be compacted (planner single turns are usually short — low risk). situational joins input below.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task-level deadline / endgame mode (injected via ctx, see taskclock.go). On the endgame turn, the task-timeout
	// planner settle words join this turn's user input as 【this turn's operating instructions】 (along with situational)
	// so it only judges goals finally and produces no new intents.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【Task endgame settlement (special instructions for this turn, overriding the normal planning flow above)】：" + resolveTaskTimeoutWrapup("planner")
	}
	// this task's working directory <workDir>/tasks/<taskID>; create it first.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // operation constraints (if any) injected into the system prompt, bounding exploration
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner has no wall-clock budget of its own; with a deadline, squeeze MaxDuration down to the remainder so a running
	// planning turn settles at task time (timeout → task-timeout words, steps → per-run words).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // via the recording proxy for traceability; loads the proxy CA to verify MITM re-signed HTTPS certs
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Web search (optional). ddgs needs no key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy is a separate egress proxy (http/https/socks5), unrelated to the traffic-recording MITM proxy; empty = direct.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash child commands use the proxy by default + trust the CA
		WorkingDir:            taskDir,                              // this task's working directory <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=unlimited; with a deadline = time left to deadline
		Compaction:            compactionConfig(p.compactionWindow()),
		// planning todos shared across wakes: keep serial chains across turns (session is fresh, store isn't).
		Todos: p.todoFor(ts.ID()),
		// hitting 【this turn's】 step budget → SDK runs settlement: land this turn's worked-out conclusions (dispatchable add_intents,
		// provable prove_goals, serial chains into TodoWrite) instead of stopping planning — the planner keeps being woken.
		// when clamped (squeezed by the task deadline), PromptByReason instead (see wrapupSettlementForTask).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // when the profile selects non-streaming, go through Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = send no cap, the server default decides
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// experimental: when enabled, noa takes over context compaction (archives land together under <workDir>/noa/<SessionID>, persisted).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Posture (just-finished intents + full graph) now joins this turn's user input (see input below). user also carries
	// instructions + cross-wake todos (todos are the model's own planning scratch — regenerable, user-side is fine).
	// The opener splits on "does this turn carry a concrete change": change → point at the 【actual changes】 block below; no change
	// (heartbeat patrol / hint / resume etc.) → don't falsely claim "the graph changed"; instead prompt a drive-by review of running intents.
	lead := "Fresh concrete changes (see 【Actual changes firing this round】 below) — plan next steps from them:"
	if len(triggers) == 0 {
		lead = "This turn is a **scheduled patrol (heartbeat) / no-concrete-change-signal** wake — the graph may have nothing new. Also review running intents on the way: steer drifting or stalled ones with steer_work, stop fully-wrong directions with kill_work; then judge goals and decide whether to add directions:"
		// On a heartbeat/no-change wake, if the whole graph has no open or running intent left → exploration stalled (no worker
		// running, no queued direction). Tell the planner explicitly and force it to add fresh directions this turn — don't idle
		// a turn on reviewing running intents alone.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This turn is a **scheduled patrol (heartbeat)** wake, and there are currently **no open or running intents at all** — no worker running, no queued direction: exploration has stalled. You **must** produce one or more new goal-pushing intents this turn that **don't duplicate** existing ones (zero intents is forbidden); first judge from the posture below whether goals are already met, and if not, add directions immediately:"
		}
	}
	input := lead + situational + "\n\nJudge goals from the posture above. When a goal is 【genuinely met】 (goal outcome in hand / goal finding confirmed), mark each with prove_goal. **Hard floor: whenever goals are still unmet AND no open or running intent exists (frontier_open=0 and running_intents empty), this turn must produce at least one goal-pushing intent — with no running work to wait for and no queued direction, 0 intents = stalled task. Only when open/running intents are already advancing, or goals are met, may this turn produce no new intents.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration now interrupts running tools at the wall clock and settles on the spot (on the live ctx); a stuck single turn no
	// longer bypasses settlement, no external hard-ctx backstop needed. ctx only carries pause / kill / shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
