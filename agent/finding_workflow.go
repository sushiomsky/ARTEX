package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Finding ID convention**: finding_id is the standalone finding-record ID; finding_node_id is the exploration-node ID. The id returned by list_findings / list_task_findings / node_detail / get_task_node_detail stays the exploration-node ID — read the standalone ID from the finding_id in the same response. get_finding_traffic / bind_finding_traffic take the standalone finding_id. The legacy update_finding_report finding_id parameter still takes finding_node_id. Don't feed report_finding's first-line number to the evidence tools, and don't guess other numbers after an ID error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default the report agent verifies and binds traffic before writing the report. Reporters keep the verification commands, key output, existing real traffic IDs and their roles in evidence, so the report agent can check them against execution records; no extra packet hunting just for binding. Compatible with explicit instant binding: traffic_refs or evidence_hint_id may submit verified references, the latter reading the structured references of the task's designated hint; any invalid one fails the whole report. TCP / packetless cases don't need these optional parameters. Returns finding_id and finding_node_id for the standalone record and the exploration node."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing off a confirmed finding, keep the verified traffic's ID, role, note, and order in the corresponding prompt's traffic_refs (single item at top level, batches inside the matching hints elements), and state in text which finding it proves. Callers must not hand off bare text while dropping existing traffic references. Unverified candidates must not be passed as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic-evidence handoff (optional)**: auto-binding is done by the report agent after the finding lands in the DB, before the report is written. Reporters should keep verification commands, key output, existing real traffic IDs and their roles in evidence, with intent_id in-task so the report agent can trace back; no extra packet hunting just for binding. When Auto / Planner reports on someone's behalf, don't drop the executor's existing references. add_hint / add_task_hint can hand off via traffic_refs; explicit instant binding still accepts report_finding's traffic_refs / evidence_hint_id. TCP or packetless cases register normally — never guess IDs, and never re-probe just to fill packets."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nPlatform conversations with no task context don't call report_finding directly; hand off via add_task_hint to the matching existing task, which its task agent registers, then verify with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore judging goals complete, finish reporting/handing off evidence already in hand. Don't end the task or cancel Workers just because the finding is text-registered while evidence handoff is still pending; packetless cases require no waiting or forced capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Pre-report auto traffic linking (enabled)**: you verify and bind traffic for this run's finding, then write the report. First get the concrete finding_id and finding_node_id from report_finding's returned JSON or get_task_node_detail / list_task_findings. Read the finding details, the matching intent's execution records, and the existing evidence list, preferring the reporter's handed-off real IDs. If this verification was HTTP and traffic tools are available, filter candidates with traffic_search, then verify each request/response with traffic_get to confirm it really supports the finding; domains and timestamps only filter, they prove nothing about ownership. Link confirmed evidence in reproduction order with bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting with a stated role. Only touch this finding — never re-create findings or re-probe targets. After a successful bind, call get_finding_traffic again for the latest version, read the bodies you need, then pass the actually-read version as evidence_version to update_finding_report (whose finding_id parameter still takes finding_node_id). Existing bindings need no re-adding. For TCP, uncaptured traffic, unavailable tools, or no exact match, skip auto-binding, write the report from text/command evidence with the reason stated, and never guess to fill traffic. A failed bind is never claimed as success; keep existing evidence and explain the unbound reason in the report."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references for the specific finding in this prompt, order kept; after handoff report_finding can carry them via evidence_hint_id.", "items": obj(map[string]any{"traffic_id": str("Real traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("What conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be this task's hint node (inherited hints can't bind directly)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
