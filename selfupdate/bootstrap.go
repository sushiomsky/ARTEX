package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv allows the child process pulled up by the smoke test to directly skip Bootstrap.
//
// Strictly speaking, nothing will happen if you don't add it: os.Executable() of the child process is derived from artex.new
// All paths are prefixed with .new and the real upgrade file cannot be touched. But relying on such coincidences is too fragile,
// The explicit short circuit is clear at a glance and saves the child process a unnecessary disk probe.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action is the instruction given by Bootstrap to main.
type Action int

const (
	// Continue: Start the server as usual.
	Continue Action = iota
	// Restart: Immediately exit with ExitRestart and let the daemon script start again.
	Restart
)

// State describes the upgrade status at this startup for /api/update/check to truthfully tell the front end
// "Whether the last upgrade was successful or was rolled back".
type State struct {
	Pending     bool   // It has not been confirmed that it is stable after the replacement.
	RolledBack  bool   // This startup has just performed automatic rollback.
	FailedStage bool   // The temporary file verification/smoke failed and has been discarded.
	Detail      string // A one-sentence explanation for users
}

// Bootstrap runs at the very beginning of main and must be called before any listening ports and before opening the database.
//
// Three situations:
//
//	① There is a temporary file artex.new → Verification + smoke. If it passes, it will be replaced and asked to restart; if it fails, it will be discarded and continue to run the old version.
//	② Only the marked file remains → It means that the installation has just been completed, and one attempt has been accumulated; if it fails multiple times in a row, it will be rolled back.
//	③ Nothing → Normal startup
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] Skipping bootstrapping: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged handles the situation of "staged files exist": if the verification passes, it will be replaced, if it fails, it will be discarded.
//
// This is the only place where the executable file will be covered in the entire upgrade link, and it is also the last gate - smoke test blocks it.
// Problems such as download corruption, wrong architecture selection, and missing dynamic links. Once a binary that cannot run is released,
// The daemon script will tirelessly pull it up repeatedly, and the Go code has no chance to run, and automatic rollback is impossible.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] The temporary new version failed the verification and has been discarded. Continue to run the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "The new version failed to verify and has been discarded:" + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] Failed to change equipment, continue to run the current version: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Failed to change outfit:" + err.Error()}
	}

	// The changeover was successful. Keep the mark and leave it to the next startup (running the new version) to confirm whether it is stable.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] Failed to write upgrade mark (lost automatic rollback capability): %v", err)
	}
	log.Printf("[update] Changed to %s, exit to restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback handles "startup after replacement": cumulative number of attempts, if the limit is exceeded, the old version will be replaced.
//
// The count is only incremented after the Go code is run, so it covers "can execute but crashes during initialization"
// (Configuration incompatibility, port occupied, DB migration crashed) This type of failure; "cannot exec at all" caused by the problem before the replacement
// The smoke test blocks it, and the two together are complete.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// If the rollback fails, don't restart again, otherwise you will get stuck in an infinite restart. Clear the mark,
			// Let the process start in its current state - if it cannot be started, the user can at least see the reason in the log.
			log.Printf("[update] The new version failed to start %d times in a row, and the rollback failed: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "The new version fails to start and the rollback fails:" + err.Error()}
		}
		log.Printf("[update] The new version failed to start %d times in a row. It has been rolled back to %s. Exit to restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("New version failed to start and has been rolled back to %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] Failed to update upgrade mark: %v", err)
	}
	log.Printf("[update] The new version is starting (%d/%d attempt), the upgrade will be confirmed after stable operation",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle confirms that the new version is running stably and clears the upgrade mark.
//
// Delayed call by main after the HTTP listener: Surviving this time counts, otherwise the mark remains in place,
// The next startup will continue to accumulate the number of attempts until a rollback is triggered.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // Not a boot after upgrade, nothing to do
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] Failed to clear upgrade mark: %v", err)
		return
	}
	log.Printf("[update] The new version is running stably and the upgrade is completed (the previous version remains as %s)", p.Old)
}

// SettleDelay is the running time required to determine "the new version has survived".
const SettleDelay = 30 * time.Second

// verifyStaged Verify the staged file: first compare SHA256, and then actually pull it up and run it again.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("Read checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("Calculate checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 mismatch (download corrupted or tampered with)")
	}
	return smokeTest(p.New)
}

// smokeTest pulls up the new binary with -h to confirm that it actually executes on the current system.
// This can prevent download truncation, wrong architecture selection (exec format error), missing dependencies and other major types of problems.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("Grant execution permission: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("Smoke test timeout (new binary unresponsive)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("Smoke test failed: %v: %s", err, snippet)
	}
	return nil
}

// swap replaces the current binary with a new temporary version.
//
// Both Unix and Windows allow rename of a running executable file (Windows prohibits deletion and
// Coverage, rename is not among them), so there is no need to divide the platform here, and there is no need to stop yourself first.
func swap(p Paths) error {
	// Windows rename will not overwrite existing targets, and the .old left over from the previous upgrade must be cleared first.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Clean old backup %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("Backup current version: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// The replacement failed but the current version has been moved and must be put back as is, otherwise there will be no executable file at the next startup.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("Failed to load new version (%v) and failed to restore current version: %w", err, rerr)
		}
		return fmt.Errorf("Load new version: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback replaces the old version of the swap backup.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("No rollback backup %s: %w", p.Old, err)
	}
	// Move the new version that cannot be loaded to .failed for troubleshooting instead of deleting it directly.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("Removal failed version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("Restore old version: %w", err)
	}
	return nil
}

// Rollback is the implementation of /api/update/rollback: actively returning to the previous version.
// Only the costume change is done, and the restart is also left to the daemon script (the caller then exits with ExitRestart).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("There is no previous version to roll back (" + p.Old + " does not exist)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("Previous version cannot be executed, rollback refused: %w", err)
	}
	// Swap current and backup: after rolling back, you can roll back again.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("Remove current version: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("Load previous version: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] Failed to organize backup after rollback (does not affect operation): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup reports whether there is a previous version that can be rolled back, allowing the front end to decide whether to display a rollback button.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Unknown version"
	}
	return s
}
