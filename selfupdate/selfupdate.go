// Package selfupdate implements ARTEX's page is updated with one click: pull the new version from GitHub Release
// Binaries, verification, temporary storage, and atomic replacement at next startup.
//
// Overall division of labor (see start.sh / start.bat):
//
//	Startup script = fool's daemon loop, only responsible for "after the process exits, press the exit code to decide whether to start again"
//	This package = all error-prone logic (download/SHA256 verification/smoke/replacement/failure rollback)
//
// The reason why I put the dressing in Go instead of a script is because the sha256 checksum smoke test is on sh and bat.
// You need to write two sets (sha256sum / shasum / certutil), and this is precisely the part that cannot go wrong-replace one
// If the binary cannot be run, the daemon will faithfully pull it up repeatedly, and the user can only save it manually on the machine.
//
// A complete upgrade requires three process starts:
//
//	① The old version of the server receives /api/update/apply → download and verify → temporarily store artex.new → exit 75
//	② The script re-launches the old version → Bootstrap finds artex.new → Verification + Smoke → Replace → exit 75
//	③ The script is re-launched, and it is now a new version → Bootstrap records an attempt → clears the mark after successful startup
//
// If any step fails, you will be returned to the old version: ② If the verification fails, delete the temporary file and continue running the old version; ③ If you fail to survive 3 times in a row
// Clear the mark (it will crash if you can't get up), and artex.old will be automatically replaced.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the exit code (EX_TEMPFAIL) of "please daemon restart me". Start the script to see it
// Just run again immediately, not counting the collapse retreat. 0 means the user stopped normally (the script exits the loop), the rest are considered crashes.
const ExitRestart = 75

// maxAttempts is the number of boot attempts allowed after reloading. Every time the new version is started, the count will be +1. Survive
// settleDelay clears the mark; crashing maxAttempts times means that the new version cannot be started at all and will automatically roll back.
const maxAttempts = 3

// Paths are all files involved in an upgrade, which are hung in the directory where the executable file is located.
// Deliberately do not use CWD: the working directory of the service runtime may be / or any path. Using CWD will cause the temporary file to fall into
// Elsewhere, the dressing logic simply fails.
type Paths struct {
	Dir     string // The directory where the executable file is located
	Current string // The currently running binary artex/artex.exe
	New     string // Temporary new version artex.new / artex.new.exe
	Sum     string // New version of sha256 (hex) artex.new.sha256 / artex.new.exe.sha256
	Old     string // The old version of artex.old / artex.old.exe backed up before the replacement
	Marker  string // Upgrade status tag artex.upgrade.json
}

// ResolvePaths deduces all upgrade paths based on the current executable file.
//
// On Windows, .new/.old must also have the .exe suffix, otherwise the smoke test and execution after replacement will fail.
// So first remove the suffix and then spell it out so that the naming of the two platforms is symmetrical.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("Locate executable file: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // ".exe" on Windows, usually empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// The marker records the progress of an outfit change and is used to trigger automatic rollback when the new version cannot be launched.
type marker struct {
	From     string `json:"from"`     // Version before upgrade
	To       string `json:"to"`       // target version
	Attempts int    `json:"attempts"` // The number of startup attempts after the replacement
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged clears the staged files. Use it if the installation is successful, the verification fails, or the user cancels to avoid residual
// artex.new is retried on next startup.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions Compares two version numbers, returning -1/0/1 (a<b / a==b / a>b).
// ok=false means that at least one side is not a comparable version number (such as "dev" for a local development build or
// "0.3.7-2-gabc1234-dirty" output by git describe), the caller should disable one-click update at this time,
// Otherwise, the build under development will be "upgraded" to the official version, overwriting uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses the version number in the form of "v0.3.7" / "0.3.7" as [3]int.
//
// Only pure three-part expressions are accepted: build.sh is output by git describe when building without a tag.
// Versions with suffixes such as "0.3.7-2-gabc1234" must be judged as incomparable instead of being treated as
// 0.3.7 - Otherwise the development build will be misjudged as "already latest" or overwritten by the official version.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether a process is running in a container. The container writable layer is written under Docker.
// `docker compose up -d` Rebuilding the container will return to the version that comes with the image - this is expected behavior
// (Users were already pulling new images at that time), but the front-end must be able to speak clearly based on this.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
