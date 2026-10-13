package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the model-driven context-compression mechanism introduced in norma v0.4.0,
// toggled by the user in system settings as a platform experiment. It is mutually
// exclusive with built-in compaction: noaadapter.Enable is the only entry point — one call
// mounts the context takeover (Compactor), the Compress tool, and three resident prompts;
// never calling Enable means off (built-in compaction keeps working). The switch is resolved
// by each agent's injected noaEnabledFn once per run, so toggling only affects runs started
// afterwards — no agent rebuild needed.

// enableNoa wires noa into opts when the resolver reports enabled. archiveRoot is the persistence base for
// compression originals (taken from the global workDir; every agent lands under <workDir>/noa instead of scattering
// across task/intent dirs); sessionID names the archive subdirectory under it (globally unique, so no collisions
// within one base dir).
//
// noa is experimental: a wiring failure must never interrupt a real task. On error, report via onWarn and fall back to built-in compression.
// On success, clear opts.Compaction so agentcore doesn't warn about "two context managers set at once".
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa compression enable failed, falling back to built-in compression:" + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but agentcore warns every time both coexist; clear it explicitly.
	opts.Compaction = nil
}
