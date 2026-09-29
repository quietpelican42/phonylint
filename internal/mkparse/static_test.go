package mkparse

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseStatic_KillerbeezPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY=all clean docs\n\nall: docs\n\ndocs:\n\t# Defer to the Makefile in the docs directory\n\tmake -C docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatalf("ParseStatic: %v", err)
	}
	if len(mf.PhonyNames) != 0 {
		t.Errorf("PhonyNames = %v, want empty (=. is a variable assignment, not a declaration)", mf.PhonyNames)
	}
	if len(mf.Specials) != 1 {
		t.Fatalf("Specials = %#v, want 1 entry", mf.Specials)
	}
	sp := mf.Specials[0]
	if sp.Name != ".PHONY" || !sp.AsVar || sp.Op != "=" {
		t.Errorf("Specials[0] = %#v, want Name=.PHONY AsVar=true Op==", sp)
	}

	var docsRule *Rule
	for i := range mf.Rules {
		for _, tgt := range mf.Rules[i].Targets {
			if tgt == "docs" {
				docsRule = &mf.Rules[i]
			}
		}
	}
	if docsRule == nil {
		t.Fatal("no rule found for target docs")
	}
	if !docsRule.HasRecipe {
		t.Error("docs rule should have a recipe")
	}
	if len(docsRule.NormalPrereqs) != 0 {
		t.Errorf("docs NormalPrereqs = %v, want none", docsRule.NormalPrereqs)
	}
}

func TestParseStatic_RealPhonyDeclaration(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY: docs clean\n\ndocs:\n\techo hi\n\nclean:\n\techo clean\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !mf.PhonyNames["docs"] || !mf.PhonyNames["clean"] {
		t.Errorf("PhonyNames = %v, want docs and clean", mf.PhonyNames)
	}
	if len(mf.Specials) != 1 || mf.Specials[0].AsVar {
		t.Errorf("Specials = %#v, want one non-var .PHONY entry", mf.Specials)
	}
}

func TestParseStatic_Misspellings(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"lowercase", ".phony: all\n"},
		{"phoney", ".PHONEY: all\n"},
		{"missingDot", "PHONY: all\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "Makefile", c.line+"\nall:\n\techo hi\n")
			mf, err := ParseStatic(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(mf.Specials) != 1 {
				t.Fatalf("Specials = %#v, want exactly 1", mf.Specials)
			}
			if mf.Specials[0].Name == ".PHONY" {
				t.Errorf("Specials[0].Name = %q, should not be exactly .PHONY", mf.Specials[0].Name)
			}
		})
	}
}

func TestParseStatic_OrderOnlyPrereqs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "docs: | prep\n\techo hi\n\nprep:\n\techo prep\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var docsRule *Rule
	for i := range mf.Rules {
		for _, tgt := range mf.Rules[i].Targets {
			if tgt == "docs" {
				docsRule = &mf.Rules[i]
			}
		}
	}
	if docsRule == nil {
		t.Fatal("docs rule not found")
	}
	if len(docsRule.NormalPrereqs) != 0 {
		t.Errorf("NormalPrereqs = %v, want none", docsRule.NormalPrereqs)
	}
	if len(docsRule.OrderPrereqs) != 1 || docsRule.OrderPrereqs[0] != "prep" {
		t.Errorf("OrderPrereqs = %v, want [prep]", docsRule.OrderPrereqs)
	}
}

func TestParseStatic_NormalPhonyPrereqExempts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY: prep\nprep:\n\techo prep\n\ndocs: prep\n\techo docs\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var docsRule *Rule
	for i := range mf.Rules {
		for _, tgt := range mf.Rules[i].Targets {
			if tgt == "docs" {
				docsRule = &mf.Rules[i]
			}
		}
	}
	if docsRule == nil {
		t.Fatal("docs rule not found")
	}
	if len(docsRule.NormalPrereqs) != 1 || docsRule.NormalPrereqs[0] != "prep" {
		t.Errorf("NormalPrereqs = %v, want [prep]", docsRule.NormalPrereqs)
	}
}

func TestParseStatic_Include(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "include extra.mk\n\nall: build\n\techo all\n")
	writeFile(t, dir, "extra.mk", "build:\n\techo build\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range mf.Rules {
		for _, tgt := range r.Targets {
			if tgt == "build" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("build target from include not found; rules=%#v", mf.Rules)
	}
	if len(mf.Files) != 2 {
		t.Errorf("Files = %v, want 2", mf.Files)
	}
}

func TestParseStatic_MultipleTargetsOneRule(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "foo bar: baz\n\techo hi\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Rules) != 1 {
		t.Fatalf("Rules = %#v, want 1", mf.Rules)
	}
	if len(mf.Rules[0].Targets) != 2 {
		t.Errorf("Targets = %v, want [foo bar]", mf.Rules[0].Targets)
	}
}

func TestParseStatic_InlineRecipe(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "docs: ; echo inline\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Rules) != 1 || !mf.Rules[0].HasRecipe {
		t.Errorf("expected one rule with a recipe, got %#v", mf.Rules)
	}
}

func TestParseStatic_TrailingCommentStripped(t *testing.T) {
	// Regression, found scanning etcd-io/bbolt and docker/compose's real
	// Makefiles: a trailing `# comment` on a .PHONY line (or any rule
	// header) must not be parsed as part of the prerequisite/target list.
	dir := t.TempDir()
	writeFile(t, dir, "Makefile",
		".PHONY: test-robustness # Running robustness tests requires root permission for now\n\ntest-robustness:\n\techo hi\n\n.PHONY: build ## Build the thing\nbuild:\n\techo build\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"test-robustness": true, "build": true}
	if len(mf.PhonyNames) != len(want) {
		t.Fatalf("PhonyNames = %v, want exactly %v", mf.PhonyNames, want)
	}
	for k := range want {
		if !mf.PhonyNames[k] {
			t.Errorf("missing %q in PhonyNames = %v", k, mf.PhonyNames)
		}
	}
}

func TestParseStatic_EscapedHashNotAComment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY: weird\\#name\n\nweird\\#name:\n\techo hi\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !mf.PhonyNames["weird\\#name"] {
		t.Errorf("PhonyNames = %v, want weird\\#name preserved (escaped #, not a comment)", mf.PhonyNames)
	}
}

func TestParseStatic_PatternRuleIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "%.o: %.c\n\tgcc -c $<\n")
	mf, err := ParseStatic(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Rules) != 1 || !mf.Rules[0].IsPattern {
		t.Fatalf("expected one pattern rule, got %#v", mf.Rules)
	}
}
