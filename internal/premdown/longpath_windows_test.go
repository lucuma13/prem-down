package premdown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// longDir returns an absolute directory well past MAX_PATH (260) that does not
// exist yet, built from components each safely under the 255-character limit
// so only the total length is being exercised.
func longDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Registered after TempDir so it runs first (cleanups are LIFO): remove the
	// tree via \\?\ so a broken long-path setup can't fail the cleanup too.
	root := dir
	t.Cleanup(func() { _ = os.RemoveAll(extended(root)) })
	for len(dir) < 300 {
		dir = filepath.Join(dir, strings.Repeat("d", 60))
	}
	return dir
}

// extended prefixes an absolute path with \\?\ so test setup succeeds whatever
// the system long-path policy, leaving only the code under test exposed to it.
func extended(p string) string { return `\\?\` + p }

func logLongPathPolicy(t *testing.T) {
	t.Helper()
	out, _ := exec.Command("reg", "query", `HKLM\SYSTEM\CurrentControlSet\Control\FileSystem`, "/v", "LongPathsEnabled").CombinedOutput() //nolint:gosec // G204: constant arguments
	t.Logf("policy: %s", strings.TrimSpace(string(out)))
}

func TestDowngradeLongPath(t *testing.T) {
	logLongPathPolicy(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "fixture_ppro26.prproj")) //nolint:gosec // G304: fixed test fixture
	if err != nil {
		t.Fatal(err)
	}
	dir := longDir(t)
	src := filepath.Join(dir, "project"+PrprojExt)
	writeFile(t, extended(src), string(fixture))
	t.Logf("src length: %d", len(src))

	dst := OutputPath(src)
	if err := silent().Downgrade(src, dst, 0, false); err != nil {
		t.Fatalf("Downgrade on a %d-character path: %v", len(src), err)
	}
	if _, err := os.Stat(extended(dst)); err != nil {
		t.Errorf("output not written: %v", err)
	}
}

func TestDowngradeProductionLongPath(t *testing.T) {
	logLongPathPolicy(t)
	src := filepath.Join(longDir(t), "MyProduction")
	writeFile(t, extended(filepath.Join(src, "MyProduction"+ProdsetExt)), prodset2026)
	writeFile(t, extended(filepath.Join(src, "Untitled"+PrprojExt)), prodProject)
	writeFile(t, extended(filepath.Join(src, "subfolder", "nested"+PrprojExt)), prodProject)
	t.Logf("src length: %d", len(src))

	dst := OutputDir(src)
	if err := silent().DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatalf("DowngradeProduction on a %d-character path: %v", len(src), err)
	}
	if _, err := os.Stat(extended(filepath.Join(dst, "subfolder", "nested"+PrprojExt))); err != nil {
		t.Errorf("output not written: %v", err)
	}
}
