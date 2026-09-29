package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not found on PATH; skipping end-to-end test that needs real GNU Make")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runMake(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("make", args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func runPhonylint(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	bin := builtBinary(t)
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("running phonylint: %v", err)
	return "", -1
}

var cachedBinary string

// TestMain builds the phonylint binary once, into a temp dir that
// outlives any single test's t.TempDir(), and removes it when the whole
// package's tests are done.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "phonylint-e2e-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "phonylint")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("go build: " + err.Error() + "\n" + string(out))
	}
	cachedBinary = bin
	os.Exit(m.Run())
}

func builtBinary(t *testing.T) string {
	t.Helper()
	if cachedBinary == "" {
		t.Fatal("binary not built by TestMain")
	}
	return cachedBinary
}

// TestE2E_SilentSkip_ThenFixed reproduces the core bug end to end: with
// the Makefile as-is, real GNU Make silently skips the recipe and exits
// 0; after applying phonylint's suggested fix (declaring the target
// .PHONY), the recipe actually runs.
func TestE2E_SilentSkip_ThenFixed(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	broken := ".PHONY=all clean docs\n\nall: docs\n\ndocs:\n\t@echo DOCS_RECIPE_RAN\n"
	writeFile(t, dir, "Makefile", broken)
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := runMake(t, dir, "docs")
	if strings.Contains(out, "DOCS_RECIPE_RAN") {
		t.Fatalf("expected the recipe NOT to run on the broken Makefile, got: %s", out)
	}
	if !strings.Contains(out, "is up to date") {
		t.Fatalf("expected make's \"is up to date\" message, got: %s", out)
	}

	stdout, code := runPhonylint(t, "-C", dir, "-format", "json")
	if code != 1 {
		t.Fatalf("phonylint exit code = %d, want 1 (findings); output: %s", code, stdout)
	}
	if !strings.Contains(stdout, "phony-shadowed-by-path") {
		t.Fatalf("expected phony-shadowed-by-path finding, got: %s", stdout)
	}
	if !strings.Contains(stdout, "phony-assignment-typo") {
		t.Fatalf("expected phony-assignment-typo finding, got: %s", stdout)
	}

	fixed := ".PHONY: all docs\n\nall: docs\n\ndocs:\n\t@echo DOCS_RECIPE_RAN\n"
	writeFile(t, dir, "Makefile", fixed)
	out = runMake(t, dir, "docs")
	if !strings.Contains(out, "DOCS_RECIPE_RAN") {
		t.Fatalf("expected the recipe to run after the fix, got: %s", out)
	}

	stdout, code = runPhonylint(t, "-C", dir, "-format", "json")
	if code != 0 {
		t.Fatalf("phonylint exit code after fix = %d, want 0; output: %s", code, stdout)
	}
}

// TestE2E_NegativeControl proves phonylint stays quiet (of errors and
// warnings) on a Makefile that does everything correctly.
func TestE2E_NegativeControl(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY: all clean docs test\n\nall: docs\n\t@echo all\n\ndocs:\n\t@echo docs\n\nclean:\n\t@echo clean\n\ntest:\n\t@echo test\n\nbin/app: main.c\n\tgcc -o bin/app main.c\n")
	// Deliberately no docs/ directory here: docs is correctly declared
	// .PHONY and nothing on disk shadows it, so this must stay quiet.
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "main.c", "int main(){return 0;}\n")

	out := runMake(t, dir, "docs")
	if !strings.Contains(out, "docs") {
		t.Fatalf("expected recipe to run on correctly phony'd target, got: %s", out)
	}

	stdout, code := runPhonylint(t, "-C", dir, "-format", "json")
	if code != 0 {
		t.Fatalf("phonylint exit code = %d, want 0 on a clean Makefile; output: %s", code, stdout)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("expected no findings, got: %s", stdout)
	}
}

// TestE2E_Killerbeez reproduces the real grimm-co/killerbeez Makefile
// pattern verbatim (fetched via `gh api repos/grimm-co/killerbeez/contents/Makefile`).
func TestE2E_Killerbeez(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY=all clean docs\n\nall: docs\n\ndocs:\n\t# Defer to the Makefile in the docs directory\n\tmake -C docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "docs"), "Makefile", "all:\n\t@echo docs-submake-ran\n")

	out := runMake(t, dir, "docs")
	if strings.Contains(out, "docs-submake-ran") {
		t.Fatalf("expected killerbeez's docs target to be silently skipped, got: %s", out)
	}
	if !strings.Contains(out, "is up to date") {
		t.Fatalf("expected \"is up to date\", got: %s", out)
	}

	stdout, code := runPhonylint(t, "-C", dir)
	if code != 1 {
		t.Fatalf("phonylint exit code = %d, want 1; output: %s", code, stdout)
	}
	for _, want := range []string{"phony-shadowed-by-path", "phony-assignment-typo", "docs"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected output to mention %q, got: %s", want, stdout)
		}
	}
}

func TestE2E_ExecMode_MatchesStatic_OnKillerbeezPattern(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY=all clean docs\n\nall: docs\n\ndocs:\n\t@echo docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, code := runPhonylint(t, "-C", dir, "-exec", "-format", "json")
	if code != 1 {
		t.Fatalf("phonylint -exec exit code = %d, want 1; output: %s", code, stdout)
	}
	if !strings.Contains(stdout, "phony-shadowed-by-path") {
		t.Fatalf("expected phony-shadowed-by-path from -exec mode, got: %s", stdout)
	}
}
