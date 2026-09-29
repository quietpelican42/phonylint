package mkparse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not found on PATH; skipping test that needs real GNU Make")
	}
}

func TestParseDatabase_PhonyAnnotation(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", ".PHONY: clean\n\nall: docs\n\techo all\n\ndocs:\n\techo docs\n\nclean:\n\techo clean\n")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mf, err := ParseDatabase(ctx, dir, []string{filepath.Join(dir, "Makefile")})
	if err != nil {
		t.Fatalf("ParseDatabase: %v", err)
	}

	var docsRule, cleanRule *Rule
	for i := range mf.Rules {
		for _, tgt := range mf.Rules[i].Targets {
			if tgt == "docs" {
				docsRule = &mf.Rules[i]
			}
			if tgt == "clean" {
				cleanRule = &mf.Rules[i]
			}
		}
	}
	if docsRule == nil {
		t.Fatal("docs rule not found in database output")
	}
	if docsRule.PhonyByDB {
		t.Error("docs should not be marked phony by the database (it isn't declared .PHONY)")
	}
	if cleanRule == nil {
		t.Fatal("clean rule not found in database output")
	}
	if !cleanRule.PhonyByDB {
		t.Error("clean should be marked phony by the database (it is declared .PHONY)")
	}
	if !mf.PhonyNames["clean"] {
		t.Errorf("PhonyNames = %v, want clean", mf.PhonyNames)
	}
}

func TestParseDatabase_ResolvesIncludesAndConditionals(t *testing.T) {
	requireMake(t)
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "include extra.mk\n\nifeq (1,1)\nall: build\n\techo all\nendif\n")
	writeFile(t, dir, "extra.mk", "build:\n\techo build\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mf, err := ParseDatabase(ctx, dir, []string{filepath.Join(dir, "Makefile")})
	if err != nil {
		t.Fatalf("ParseDatabase: %v", err)
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
		t.Errorf("build target (from include, inside a true ifeq) not found; rules=%#v", mf.Rules)
	}
}

func TestParseDatabase_ShellSideEffect_DocumentedTradeoff(t *testing.T) {
	// This is the reason -exec is opt-in and not the default: make -p
	// evaluates $(shell ...) exactly like a real build would.
	requireMake(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "side-effect-ran")
	writeFile(t, dir, "Makefile", "FOO := $(shell touch "+marker+")\nall:\n\techo hi\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ParseDatabase(ctx, dir, []string{filepath.Join(dir, "Makefile")}); err != nil {
		t.Fatalf("ParseDatabase: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected $(shell touch ...) to have run during `make -pn` (confirms the documented tradeoff)")
	}
}
