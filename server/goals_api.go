package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// Overview of the manual CRUD interface for Goal Management. Write the same batch of goal nodes as the set_goals tool on the agent side,
// But the entrance is for humans to add, delete, and modify directly on the UI; after addition/modification, the "resurrection task" logic is reused (admitTask resume:
// Final state → running, unpause, queue if necessary), delete without resurrection (according to product decision). Every change handler goes
// beginTaskOperation/decInflight, avoid race conditions with task deletion (consistent with intent CRUD).

// listGoals returns all the goals of this task (text/vulnclass/state is disassembled) for the target to manage card rendering.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal artificially adds a goal: drop into the library (hang under the task root spawns) → record a "new goal" to trigger wake-up
// planner → resurrect the task, allowing the planner to re-judge whether it has been achieved based on the new goal.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "The task is being deleted and new targets cannot be added.")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Target content cannot be empty")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // Remember "People added a new goal:..." to trigger and wake up the planner
	s.reviveTask(t)              // Bring completed/paused tasks back to running state and continue running
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "Read failed after target write")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal manually modify a target text (and vulnclass): change the database → note "the user modified the target from old to new"
// Trigger wake-up planner → resurrect the task, allowing the planner to adjust the direction according to the new goal.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "The task is being deleted and the target cannot be modified")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Target content cannot be empty")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Target does not exist")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // Remember "The person has modified the target from old to new" to trigger and wake up the planner
	s.reviveTask(t)                   // Consistent with the new addition: the resurrection mission is re-judged based on the new target
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "Read fails after target update")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal manually deletes a goal (hard deletion, cascading edge/anchor deletion): Delete database → record "The user deleted the goal X"
// Trigger wake-up planner to re-judge remaining targets accordingly. According to the product decision, delete the [not] resurrection task.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "The task is being deleted and the target cannot be deleted")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "Target does not exist")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // Note "The person deleted the target:..." triggers and wakes up the planner (no resurrecting the task)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
