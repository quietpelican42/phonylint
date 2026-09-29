package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/__GH_OWNER__/phonylint/internal/mkparse"
)

func hasFinding(findings []Finding, rule, target string) bool {
	for _, f := range findings {
		if f.Rule == rule && f.Target == target {
			return true
		}
	}
	return false
}

func mustParse(t *testing.T, dir, content string) *mkparse.Makefile {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mf
}

func TestAnalyze_PhonyShadowedByPath(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, "all: docs\n\techo all\n\ndocs:\n\techo docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// re-parse after creating the directory
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := Analyze(mf)
	if !hasFinding(findings, RulePhonyShadowedByPath, "docs") {
		t.Errorf("expected phony-shadowed-by-path for docs, got %#v", findings)
	}
}

func TestAnalyze_NegativeControl_DeclaredPhony(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: all clean docs\n\nall: docs\n\techo all\n\ndocs:\n\techo docs\n\nclean:\n\techo clean\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := Analyze(mf)
	for _, f := range findings {
		if f.Severity == SeverityError || f.Severity == SeverityWarning {
			t.Errorf("expected no error/warning findings on correctly phony'd Makefile, got %#v", f)
		}
	}
}

func TestAnalyze_NoPathClash_NoFinding(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, "docs:\n\techo docs\n")
	// no docs/ directory created
	findings := Analyze(mf)
	if hasFinding(findings, RulePhonyShadowedByPath, "docs") {
		t.Errorf("should not flag docs when no path clash exists: %#v", findings)
	}
	if !hasFinding(findings, RuleLikelyPhony, "docs") {
		t.Errorf("expected likely-phony warning for conventional name docs: %#v", findings)
	}
}

func TestAnalyze_PhonyAssignmentTypo(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY=all clean docs\n\nall: docs\n\ndocs:\n\techo docs\n")
	findings := Analyze(mf)
	if !hasFinding(findings, RulePhonyAssignmentTypo, ".PHONY") {
		t.Errorf("expected phony-assignment-typo, got %#v", findings)
	}
}

func TestAnalyze_PhonyMisspelled(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONEY: all\n\nall:\n\techo hi\n")
	findings := Analyze(mf)
	if !hasFinding(findings, RulePhonyMisspelled, ".PHONEY") {
		t.Errorf("expected phony-misspelled, got %#v", findings)
	}
}

func TestAnalyze_PhonyWithoutRule(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: all ghost\n\nall:\n\techo hi\n")
	findings := Analyze(mf)
	if !hasFinding(findings, RulePhonyWithoutRule, "ghost") {
		t.Errorf("expected phony-without-rule for ghost, got %#v", findings)
	}
}

func TestAnalyze_PhonyWithoutRule_PatternRuleSatisfies(t *testing.T) {
	// Regression: docker/moby's real Makefile lists `validate-%` in
	// .PHONY because `validate-%:` is a pattern rule; that must not be
	// reported as phony-without-rule.
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: validate-%\n\nvalidate-%: build\n\techo validating $*\n\nbuild:\n\techo build\n")
	findings := Analyze(mf)
	if hasFinding(findings, RulePhonyWithoutRule, "validate-%") {
		t.Errorf("pattern rule should satisfy phony-without-rule, got %#v", findings)
	}
}

func TestAnalyze_PhonyWithoutRule_ForceIdiomExempt(t *testing.T) {
	// Regression: git's real Makefile declares `.PHONY: FORCE` with no
	// FORCE: rule at all, as a deliberate always-out-of-date sentinel.
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: FORCE\n\nversion.h: FORCE\n\techo generating version.h\n")
	findings := Analyze(mf)
	if hasFinding(findings, RulePhonyWithoutRule, "FORCE") {
		t.Errorf("FORCE idiom should be exempt from phony-without-rule, got %#v", findings)
	}
}

func TestAnalyze_LikelyPhony(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, "clean:\n\trm -rf build\n")
	findings := Analyze(mf)
	if !hasFinding(findings, RuleLikelyPhony, "clean") {
		t.Errorf("expected likely-phony for clean, got %#v", findings)
	}
}

func TestAnalyze_PhonyShouldBeFile_Info(t *testing.T) {
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: output\n\noutput:\n\techo hi > output\n")
	if err := os.WriteFile(filepath.Join(dir, "output"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := Analyze(mf)
	found := false
	for _, f := range findings {
		if f.Rule == RulePhonyShouldBeFile && f.Target == "output" {
			found = true
			if f.Severity != SeverityInfo {
				t.Errorf("phony-should-be-file severity = %s, want info", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected phony-should-be-file info finding, got %#v", findings)
	}
}

func TestAnalyze_OrderOnlyPhonyPrereq_StillFlagged(t *testing.T) {
	// Verified against real GNU Make 3.81: an order-only prerequisite,
	// phony or not, does not protect a target from the "is up to date"
	// silent skip the way a normal prerequisite does.
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: prep\nprep:\n\techo prep\n\ndocs: | prep\n\techo docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := Analyze(mf)
	if !hasFinding(findings, RulePhonyShadowedByPath, "docs") {
		t.Errorf("expected phony-shadowed-by-path despite order-only phony prereq, got %#v", findings)
	}
}

func TestAnalyze_NormalPhonyPrereq_NotFlagged(t *testing.T) {
	// Verified against real GNU Make 3.81: a normal (non order-only)
	// prerequisite that is itself phony forces the dependent target to
	// rebuild every time; it is not silently skipped.
	dir := t.TempDir()
	mf := mustParse(t, dir, ".PHONY: prep\nprep:\n\techo prep\n\ndocs: prep\n\techo docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := mkparse.ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings := Analyze(mf)
	if hasFinding(findings, RulePhonyShadowedByPath, "docs") {
		t.Errorf("should not flag docs: a normal phony prerequisite forces a real rebuild, got %#v", findings)
	}
}
