package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// For server-side parsing of the retry strategy, see docs/LLM Retry Design.md. On the fifth floor:
//   - Connection establishment/empty response/same provider security window "follows the endpoint" and can be overridden by each LLM configuration
//     Global default (if an item in the profile is left blank, it will inherit the global one, and if the global one is not configured, the built-in default will be used);
//   - Circuit breakers/intended reruns are at the process level and only have a global copy.
//
// The global strategy reads one row of DB settings at a time, and the call points are all on low-frequency paths (building provider, work ending,
// save configuration), it is not worth adding another layer of caching; the exception is the circuit breaker parameter - it needs to be read every time on the failure path, so
// Pushed to Registry by applyRetryPolicy.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// The number of times here maintains the original semantics of "0=default/negative=off": SDK's MaxRetries/
		// EmptyResponseRetries is completely isomorphic to it, just let it parse by itself.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// The default value of circuit breaker (polling cooling) is consistent with the built-in value of llmpool - it is only overridden here when "the user configures the value".
// See modelErrorRetries / modelErrorRetryBackoff in engine.go for the default values ​​for intent reruns.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— "Empty response
// "Number of retries": Both are two ways of doing the same thing. The SDK layer manages "not a single content block", and the method is to
// The same request is reissued as it is; here it refers to "only thinking, neither text nor tools". The method is to add an instruction to
// The model continues with existing thoughts (resending as is does not make sense for this kind of idling determined by the context shape). short caliber
// The difference is that the SDK is based on "whether an event has been yielded", while the increment itself is an event - but the user configuration
// What I want to express when "empty response is retried a few times" is "the model will try again if it does not produce substantial content". The two layers share the same number of times.
// Only then can I match this mentality.
//
// Read the global policy instead of the coverage of a certain profile: a run may change profiles due to failover, and this
// It is the total amount of the entire intention and should not change with the change of endpoints. The semantics are isomorphic to the SDK’s emptyRetries():
// 0 = Default defaultEmptyTurnNudges;-1 (negative) = Turn off idling and continue running; >0 = Use this value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
