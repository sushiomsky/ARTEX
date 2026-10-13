package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration-test goal decomposer. Your job is to identify the **final outcomes to achieve** from the user input — not to plan attack steps.

**Step one (do this before splitting goals): extract operation constraints**
From the "task goal / task description", identify the operator's explicit rules about [what operations may and may not be done], and register each via set_constraints (skip constraint extraction if the description/goal involves no operation constraints):
- type=deny: forbidden operations (e.g. "no port scanning", "no write/delete on production", "no brute force", "don't touch a certain subdomain").
- type=allow: explicitly allowed/limited operation scope (e.g. "passive recon only", "only against a certain domain").
- A constraint is neither a goal nor attack steps: it bounds operation behavior.
- **Constraints must be [self-contained, with concrete targets baked in]**: replace **referring expressions** like "current target / current port / current IP / current domain / this site" with the **concrete values** from the task goal/description. Constraints are injected standalone into execution-stage prompts, where pronouns can't be resolved out of context.
  Example: goal is https://abc.example.net → write "only test abc.example.net", not "only test the current target"; "only test target port 443, scan no other ports", not "only test the current port". If the original text only says "current target" but the target address is known, fill the address in.
- **Register only constraints [explicitly stated or emphasized] in the goal/description — never invent any**; when unsure of the type, use deny (more conservative).
- If the goal/description genuinely contains no operation constraints, do **not** call set_constraints.
After registering constraints (if any), proceed to goal splitting below.

**Goal = a final deliverable/verifiable outcome**

**Not goals (never list as sub-goals)**:
- Information gathering, recon, endpoint scanning
- Vulnerability analysis and verification processes
- Attack steps, exploitation techniques
- Result verification steps

**Splitting rules**:
- The user-described final goal is singular → output one
- Multiple **mutually independent** final deliverables exist → list each
- Label vulnclass where it maps to a clear vulnerability class; leave empty for recon/business-logic goals
- Never invent goals the user didn't mention

Call set_goals to submit the result.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Extra duty: register the test asset scope**
Beyond splitting goals, also identify the **explicitly given test asset scope** from the "task goal / task description" and register it via add_task_scope (this task's authorization boundary, and the denominator of asset-test coverage). **Least-scope rule: register only the one target the user explicitly named — never widen it yourself.**
- Goal is a URL or an address with a hostname (e.g. https://xxx.example.com/path, app.example.com) → take its **full hostname**, kind=subdomain, value=full hostname.
  Example: goal https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Never** shrink a hostname with a subdomain into the root domain — registering all of example.com from seeing xxx.example.com expands scope past the user's target and violates least-scope.
- Only when the user gave a **bare root domain with no subdomain at all** (e.g. literally example.com), or explicitly says "whole site / all subdomains / all domains" → use kind=root_domain, value=example.com.
- Bare IPs or CIDRs → kind=ip / cidr, value=IP or CIDR.
- Do **not** register a company scope — the task was just created and the company usually isn't in the asset system yet, so registration would fail; company-level scope is handled in the later plan stage.
Other rules:
- Register only scope **explicitly stated** in the goal/description; never invent or infer unmentioned domains/IPs.
- reason briefly cites which sentence it came from, for audit.
- If the goal/description has no explicit asset scope, do **not** call add_task_scope.
Register scope with add_task_scope first (if any), then submit goals with set_goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (background: target scope/flag count/engagement notes).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call: no transcript store is attached, so agentcore won't hang
	// a session id on the ctx (it only hangs one when a writer exists, see agentcore.Prompt). But gateways doing
	// prompt-cache/sticky routing on the session-id header (opencode zen returns 400 without x-opencode-session
	// MissingSessionID) read exactly that ctx value — without it, "chat works, decomposition 400s".
	// Hang an explicit stable id: decomposition requests for the same exploration share it (good for cache hits), and the name
	// doesn't collide with planner/worker, so llmrec.parseSession attributes it correctly.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints is always available (no asset store needed): the body already contains the "extract operation constraints before splitting goals" step
	// (wording editable on the agent edit page); here just wire up the tool.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask description (background info, may include target scope/flag count/engagement notes; reference only, do not invent anything not mentioned in it):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// the 3 steps (extract constraints → register scope → split goals) each need one tool call; allow enough turns so set_goals isn't missed before settlement.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // when the profile selects non-streaming, go through Provider.Complete
		MaxTokens:    maxTokens,    // 0 = send no cap, the server default decides
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
