package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths creates an isolated upgrade directory. Can't use ResolvePaths() directly - that would point to tests
// In the binary itself, the executable file of go test is renamed as soon as it is run.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Dir:     dir,
		Current: filepath.Join(dir, "artex"),
		New:     filepath.Join(dir, "artex.new"),
		Sum:     filepath.Join(dir, "artex.new.sha256"),
		Old:     filepath.Join(dir, "artex.old"),
		Marker:  filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin writes an executable shell script to impersonate artex. smokeTest just pull it up with -h to see the exit code.
// The script is perfectly adequate and much faster than compiling a real binary.
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("Writing fake binary %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// Stage arranges the bin as "temporarily stored for replacement": write artex.new and its checksum.
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err := fileSHA256(p.New)
	if err != nil {
		t.Fatalf("Calculate checksum: %v", err)
	}
	if err := os.WriteFile(p.Sum, []byte(sum), 0o644); err != nil {
		t.Fatalf("Write checksum: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Reading %s: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("The fake binary uses sh script and cannot run on Windows.")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // Remove the v in build.sh and add v to the tag. Both sides must be recognized.
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // Sort numerically rather than lexicographically
		{"1.0.0", "0.99.99", 1, true},
		// Development builds must be judged as incomparable, otherwise uncommitted changes will be overwritten by the official version.
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, expects %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, expects %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// Key invariant: All upgrade files are in the same directory as the executable file. Falling to CWD will enable service-oriented operation
	// (The working directory may be /) The change completely fails.
	for name, path := range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s is not in the executable directory: %s (expected %s)", name, path, p.Dir)
		}
	}
	// .new/.old must remain .exe on Windows, otherwise smoke testing and post-replacement execution will fail.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(p.New, ".exe") || !strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("On Windows .new/.old must end with .exe: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "new", 0)

	// After the checksum is written, modify the file to simulate download damage/dropped files.
	fakeBin(t, p.New, "tampered", 0)
	if err := verifyStaged(p); err == nil {
		t.Fatal("Expected SHA256 mismatch to be rejected, passed instead")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "broken", 1) // Can be executed but exit code is non-0

	if err := verifyStaged(p); err == nil {
		t.Fatal("Expected smoke test to fail and be rejected, but passed")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err != nil {
		t.Fatalf("Write tag: %v", err)
	}

	action, st := applyStaged(p)
	if action != Restart {
		t.Fatalf("Expected Restart, got %v", action)
	}
	if !st.Pending {
		t.Error("After the change, the status should be Pending.")
	}
	if !strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex should have been replaced with the new version")
	}
	if !strings.Contains(readAll(t, p.Old), "old") {
		t.Error("Old versions should be backed up to artex.old")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("artex.new should disappear after the change.")
	}
	if _, err := os.Stat(p.Sum); !os.IsNotExist(err) {
		t.Error("The checksum file should be cleaned after the replacement")
	}
	// The mark must be retained, and the next startup (running a new version) will be counted based on it and rolled back if necessary.
	if _, ok := readMarker(p.Marker); !ok {
		t.Error("The upgrade mark should be retained after the change of equipment.")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // corrupt checksum

	action, st := applyStaged(p)
	if action != Continue {
		t.Fatalf("Expected Continue when verification failed, got %v", action)
	}
	if !st.FailedStage {
		t.Error("The status should be marked as FailedStage")
	}
	if !strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("When verification fails, the current version must not be touched.")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("The temporary files that failed the verification should be cleared, otherwise the next startup will try again.")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // Backup left over from the previous round of upgrades
	stage(t, p, "v3", 0)

	if err := swap(p); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("Should switch to v3")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("The backup should be updated to the v2 that was just replaced")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m := marker{From: "0.3.7", To: "0.3.8"}

	// The first maxAttempts launches only count cumulatively, giving new versions a chance to establish themselves.
	for i := 1; i <= maxAttempts; i++ {
		action, st := confirmOrRollback(p, m)
		if action != Continue {
			t.Fatalf("Attempt %d expected Continue, got %v", i, action)
		}
		if !st.Pending {
			t.Errorf("Attempt %d should have status Pending", i)
		}
		got, ok := readMarker(p.Marker)
		if !ok || got.Attempts != i {
			t.Fatalf("after attempt %d: attempts=%d (ok=%v), expect %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// If it crashes again, it will exceed the limit and the old version will be automatically replaced.
	action, st := confirmOrRollback(p, m)
	if action != Restart {
		t.Fatalf("Expected Restart when attempt limit exceeded, got %v", action)
	}
	if !st.RolledBack {
		t.Error("The status should be marked as RolledBack")
	}
	if !strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("Should have been rolled back to the older version")
	}
	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Error("The mark should be cleared after rollback, otherwise it will rollback infinitely")
	}
	// The version that cannot be loaded is reserved for troubleshooting and is not deleted directly.
	if _, err := os.Stat(p.Current + ".failed"); err != nil {
		t.Error("Failed versions should be retained as .failed for troubleshooting")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback() takes ResolvePaths(), where the underlying exchange semantics are directly measured.
	tmp := p.Current + ".swap"
	if err := os.Rename(p.Current, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("After rollback the current version should be v1")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("After the rollback, the backup should be v2 so that you can roll back again.")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum   = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	// The output of sha256sum is double space separated; shasum -a 256 will add * to the file name in binary mode.
	raw := linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // Exactly two fields, but the first is not a summary
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // Summary length is wrong

	out := parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"] != linuxSum {
		t.Errorf("linux entry parsing error: %v", out)
	}
	// The abstracts should be in lower case, so that they will not be misjudged as mismatches due to case when comparing.
	if got := out["artex-0.3.8-windows-amd64.zip"]; got != strings.ToLower(winSum) {
		t.Errorf("Windows entry error (* prefix should be stripped, summary should be lowercase): %q", got)
	}
	if len(out) != 2 {
		t.Errorf("Empty lines, non-summary lines, and lines of incorrect length should be ignored, resulting in %v", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("The base name in the package is artex.exe on Windows, and this use case is constructed according to Unix naming")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// The structure of the real release package: artex-<version>-<os>-<arch>/artex, plus some interference files.
	for name, body := range map[string]string{
		"artex-0.3.8-linux-amd64/README.md":           "readme",
		"artex-0.3.8-linux-amd64/skills/a.md":         "skill",
		"artex-0.3.8-linux-amd64/artex":               "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extractBinary(zipPath, dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got := readAll(t, dst); !strings.Contains(got, "exit 0") {
		t.Errorf("The extracted file is not an artex executable file: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("The decompressed binary must have an execution bit")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err := extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("An error should be reported when there is no executable file in the package.")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad := []string{
		"http://github.com/x",           // Non-HTTPS
		"https://evil.com/artex.zip",    // The domain name is not in the whitelist
		"https://github.com.evil.com/x", // suffix disguise
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw := range bad {
		u := mustParse(t, raw)
		if err := checkURL(u); err == nil {
			t.Errorf("checkURL(%q) should reject", raw)
		}
	}
	good := []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // Domain names are not case sensitive
	}
	for _, raw := range good {
		u := mustParse(t, raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) should be allowed, but an error is reported: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// The package_binary of build.sh uses artex-<version>-<os>-<arch>.zip, and the version number is
	// Removed v prefix. One character is right or wrong here, and one-click updates on all platforms will not find the asset.
	if got := AssetName("v0.3.8", "linux", "amd64"); got != "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("0.3.8", "windows", "amd64"); got != "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse %q: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Fatal("After confirming stability, the upgrade mark must be cleared.")
	}
	// Once the mark is gone, subsequent normal restarts will no longer accumulate times and rollback will not be accidentally triggered.
	if _, ok := readMarker(p.Marker); ok {
		t.Error("Tag read should fail")
	}
	// The backup should be retained, and the user can still roll it back manually.
	if _, err := os.Stat(p.Old); err != nil {
		t.Error("After confirming that it is stable, you should still keep the backup of the previous version.")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // Normal startup path, should not panic or move any files
	if _, err := os.Stat(p.Current); err != nil {
		t.Error("settle should not affect any files when unmarked")
	}
}
