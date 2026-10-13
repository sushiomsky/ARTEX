package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Settlement prompts (wrap-up / settlement prompt): when an agent is stopped for [step budget
// exhausted (MaxTurns)] or [timeout (run_seconds/MaxDuration)], the SDK's settlement phase injects this prompt
// so the agent persists identified-but-unwritten content first, then emits one summary — no dangling tail.
//
// Each agent's settlement prompt is overridable in the backend as needed (stored in agents.wrapup_prompt); empty uses the
// built-in default here. Only the [prompt body] is editable; which tools are disabled and the settle phase's own turn
// budget are fixed code policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default settlement prompts, indexed by agent key. worker reuses the historically hardcoded settleWrapUpPrompt
// (defined in worker.go); planner/mainagent each have one; misses (custom agents) take the generic fallback.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: each agent's settle phase's [own] turn budget, built-in default (overridable by backend >0).
// 10 turns each, so settling has enough steps to persist. Misses take genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your planning turn's step budget is nearly out — note this only ends [this turn]; the system will wake you again as the posture evolves to keep planning. This is not task termination, and you don't need to wrap up the whole plan here. Land the conclusions you've already worked out so this turn isn't wasted, but [don't invent intents just to settle] (zero intents this turn is a perfectly normal outcome): (1) if you've identified exploration directions that [should dispatch now], batch-submit them with one add_intent call (don't hold back what you've decided); (2) for goals proven met by some discovery/fact, call prove_goal to mark them met (don't miss any); (3) if you see a serial exploit chain needing steps, note it with TodoWrite so the next wake-up can dispatch from it. Then end the turn directly — no summary text needed."

const mainAgentWrapUpDefault = "Your step budget is nearly out; this interaction is ending. Don't start any new exploration/operations. **In one plain-text sentence on its own**, summarize current progress, key conclusions, and suggested next steps for the user."

const genericWrapUpDefault = "You are about to be terminated on budget exhaustion. First write back finished-but-unpersisted results, then **in one plain-text sentence on its own** summarize what you did and the key conclusions (this line is shown as the run's result)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-level timeout settle words ----------
//
// Two SEPARATE sets from the per-run settle words: per-run means "this run's budget is spent"; task timeout means
// "the whole task hit its deadline and is ending". The semantics are often opposite (especially planner: per-run says
// "don't stop, keep planning", task timeout says "time's up, stop planning and judge finally"). Configured only for
// worker/planner.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides for the task-timeout settle words and turns
// (wired to agents.task_timeout_wrapup_prompt / _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has hit its timeout ceiling and is ending** (not this run's budget — the entire engagement is out of time). Last chance: (1) persist EVERYTHING you've identified but not yet written back — new assets via insert_assets, exploration conclusions/facts via record_fact, confirmed findings via report_finding; (2) start no new commands/probes; (3) **close with one plain-text sentence on its own** summarizing your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The whole task has hit its timeout ceiling and is ending** (not this turn — the entire task terminates). Judge goals one last time on ALL current facts and findings: call prove_goal to mark met for goals proven by evidence (don't miss any). **Generate no new intents** (intents dispatched now would never execute). Wrap up after judging — no summary text needed."

// TaskTimeoutWrapupDefault returns an agent's built-in default task-timeout settle words (for backend placeholder/restore-default).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // unconfigured (mainagent/chat) returns empty
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. Empty means the agent has no task-timeout words
// (non worker/planner), in which case callers fall back to per-run words.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // default inherits per-run turns
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run is squeezed by the task deadline: Timeout settle = task time → task-timeout words;
//     MaxTurns settle = steps ran out inside the squeeze window with minutes left → fall back to per-run words.
//   - clamped=false → task still early: both reasons use per-run words (i.e. degenerates to wrapupSettlement).
//
// Handed to harness's PromptByReason, which picks on the spot at settle time by 【actual】 reason — no build-time mismatch.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // fallback (also what both reasons resolve to when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // task time reached
				harness.ReasonMaxTurns: perRun, // steps ran out first with task time left
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
