package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "User paused the task",
		"The user paused the task via the task control API (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was actively cancelled; in-flight intents return to frontier(open) and are re-claimed and re-executed from scratch after the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestrator agent paused the task",
		"The orchestrator agent paused this task with the pause_task tool. This Planner/Worker run was actively cancelled; in-flight intents return to frontier(open) and re-execute after resume")
	AbortTaskDeleted = cause("task_deleted", "Task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the deletion barrier has cancelled this task's running Planner, Worker, and main Agent. This run's results will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored the task's paused state",
		"At startup the backend restored the task pause from the persisted database state. This run is cancelled; normally no agents are running during recovery")
	AbortGoalMet = cause("goal_met", "Planner judged the task goal achieved",
		"Planner judged the task goal achieved and set the task to done, then cancelled still-running Workers; those intents are marked stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task-timeout settle wait ran out",
		"After the task hit timeout it waited for running Workers to settle gracefully, but the 90s drain grace was still not enough, so a hard cancel fired; intents are marked exhausted, and facts and assets written during settling are kept")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner killed this intent",
		"Planner actively killed this intent with kill_work, usually meaning the direction drifted or has no further value; the intent is marked stopped and will not be re-claimed automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "User paused this Worker intent",
		"The user paused the running Worker. This call is cancelled and the intent becomes paused; all registered intents, facts, findings, and activity records are kept, and execution restarts from scratch on resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"The user deleted the running Worker. This call is cancelled; after the Worker leaves the write zone, the server handles the intent per the user's chosen deletion mode — soft delete only marks it deleted and keeps all output, hard delete cascade-removes the intent and downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker ended normally",
		"Worker ended normally; the engine released its context resources in detachWork. This is not a run interruption; if it shows up in an interruption message, cancel and teardown raced")
	AbortPausedRaceGuard = cause("paused_race_guard", "New runs refused while task is paused",
		"While the task is paused the engine refuses to issue new execution contexts, guarding against a claim-vs-pause race that would keep starting Workers; claimed intents return to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this conversation turn",
		"The user hit stop, actively aborting this main-agent or session-agent run. Activity records already produced are kept; you can send the next message")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause aborted the conversation",
		"When the user paused the task, the running main-agent conversation was cancelled with it. Activity records already produced are kept; the turn's message is not auto-replayed on resume")
	AbortChatTurnFinished = cause("chat_turn_finished", "Conversation turn ended normally",
		"This conversation turn ended normally; the server is releasing that turn's context resources. This is not a run interruption; if it shows up in an interruption message, cancel and teardown raced")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process is shutting down",
		"Backend process received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running agents are cancelled; leftover running intents reset to open and re-execute after restart")
	AbortRunHardTimeout = cause("run_hard_timeout", "Per-run hard timeout backstop fired",
		"A single run exceeded its soft wall-clock budget plus grace, meaning a model request or some tool went long without returning, so normal turn-boundary settlement could not run. Focus on the last unreturned tool call before the interruption")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context hit deadline",
			"Upstream context hit deadline, but the setter did not attach a named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Canceller attached no named cause",
			"Upstream context was cancelled, but the canceller did not attach a named cause via context.WithCancelCause; register the cause in agent/cancelcause.go and wire up that cancel site", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
