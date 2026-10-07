// Package rules turns a parsed Makefile into a list of findings about
// .PHONY mistakes, chiefly the case where a target with a recipe shares
// its name with a real file or directory: GNU Make then reports
// "'name' is up to date" and skips the recipe, silently and permanently,
// because nothing marks the target phony.
package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/quietpelican42/phonylint/internal/mkparse"
)

// Severity levels. Exit-code and formatting logic key off these.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Rule IDs.
const (
	RulePhonyShadowedByPath = "phony-shadowed-by-path"
	RulePhonyAssignmentTypo = "phony-assignment-typo"
	RulePhonyMisspelled     = "phony-misspelled"
	RulePhonyWithoutRule    = "phony-without-rule"
	RuleLikelyPhony         = "likely-phony"
	RulePhonyShouldBeFile   = "phony-should-be-file"
)

// Finding is one reported issue.
type Finding struct {
	Rule     string
	Severity string
	Target   string
	File     string
	Line     int
	Message  string
}

// conventionalPhonyNames are target names that are almost always meant
// to be phony in real-world Makefiles.
var conventionalPhonyNames = map[string]bool{
	"all": true, "clean": true, "test": true, "lint": true, "fmt": true,
	"install": true, "build": true, "run": true, "docs": true, "help": true,
	"check": true, "release": true, "deps": true,
}

// Analyze runs every rule against mf and returns findings sorted by
// file, then line, then rule ID, for stable output.
func Analyze(mf *mkparse.Makefile) []Finding {
	var out []Finding

	// Index: which targets are actually defined by a rule (for
	// phony-without-rule), and which non-pattern, recipe-bearing targets
	// already collide with a path on disk (so likely-phony doesn't also
	// fire for something already reported as the error).
	defined := map[string]bool{}
	shadowed := map[string]bool{}

	for i := range mf.Rules {
		r := &mf.Rules[i]
		for _, t := range r.Targets {
			// Pattern-rule targets (e.g. `validate-%:`) count as
			// defining every literal name that pattern could produce;
			// phony-without-rule only needs to know a rule exists at
			// all, so record the literal spelling here too (a real-world
			// example: docker/moby's Makefile lists `validate-%` in
			// .PHONY precisely because `validate-%:` is a pattern rule).
			defined[t] = true
		}
	}

	for i := range mf.Rules {
		r := mf.Rules[i]
		if r.IsPattern || !r.HasRecipe {
			continue
		}
		for _, t := range r.Targets {
			if mf.PhonyNames[t] || r.PhonyByDB {
				continue
			}
			if len(r.NormalPrereqs) > 0 {
				// A normal (non order-only) prerequisite forces make to
				// re-evaluate the target's timestamp against it; if that
				// prerequisite is itself phony it forces a rebuild every
				// time (verified: GNU Make 3.81, a target depending on a
				// real phony prerequisite always reruns, it is never
				// reported "up to date"). Order-only prerequisites do
				// not have this effect, which is why they don't exempt a
				// target below.
				continue
			}
			if pathExists(mf.Dir, t) {
				shadowed[t] = true
				out = append(out, Finding{
					Rule:     RulePhonyShadowedByPath,
					Severity: SeverityError,
					Target:   t,
					File:     r.File,
					Line:     r.Line,
					Message: fmt.Sprintf(
						"target %q has a recipe but is not declared .PHONY, and a file or directory named %q exists next to the Makefile: `make %s` will print \"%s is up to date\" and skip the recipe, silently and every time",
						t, t, t, t),
				})
			}
		}
	}

	for _, s := range mf.Specials {
		switch {
		case s.Name == ".PHONY" && s.AsVar:
			out = append(out, Finding{
				Rule:     RulePhonyAssignmentTypo,
				Severity: SeverityError,
				Target:   ".PHONY",
				File:     s.File,
				Line:     s.Line,
				Message: fmt.Sprintf(
					"`.PHONY %s ...` assigns a variable named \".PHONY\"; it does not declare anything phony. Use `.PHONY: %s`", s.Op, joinNames(s.Names)),
			})
		case s.Name == "PHONY" && s.Op == "PHONY:":
			out = append(out, Finding{
				Rule:     RulePhonyAssignmentTypo,
				Severity: SeverityError,
				Target:   "PHONY",
				File:     s.File,
				Line:     s.Line,
				Message:  "`PHONY:` (missing the leading dot) defines an ordinary target named \"PHONY\"; it is not the special `.PHONY:` target and marks nothing phony",
			})
		case s.Name != ".PHONY":
			form := "rule"
			if s.AsVar {
				form = "variable"
			}
			out = append(out, Finding{
				Rule:     RulePhonyMisspelled,
				Severity: SeverityError,
				Target:   s.Name,
				File:     s.File,
				Line:     s.Line,
				Message: fmt.Sprintf(
					"%q looks like a misspelling of the special target \".PHONY\" (parsed as a %s, operator %q); GNU Make does not recognize it and nothing is marked phony",
					s.Name, form, s.Op),
			})
		}
	}

	for name := range mf.PhonyNames {
		// FORCE is a widely used GNU Make idiom: `.PHONY: FORCE` with
		// deliberately no `FORCE:` rule at all, used purely as a
		// prerequisite that is always considered out of date (git's own
		// Makefile does this throughout). Warning about it would be
		// noise on a large fraction of real Makefiles, not a mistake.
		if name != "FORCE" && !defined[name] {
			out = append(out, Finding{
				Rule:     RulePhonyWithoutRule,
				Severity: SeverityWarning,
				Target:   name,
				File:     primaryFile(mf),
				Message:  fmt.Sprintf(".PHONY lists %q, but no rule defines a target with that name", name),
			})
		}
		// Conventional names (test, docs, install, ...) routinely share
		// a name with an unrelated real directory (a test suite lives
		// in test/, generated docs live in docs/) while still being
		// correctly phony for an unrelated reason. Flagging those as
		// "should this be a file target" is nearly always noise, so
		// this check is restricted to names that don't already look
		// like a deliberate phony convention.
		if conventionalPhonyNames[name] {
			continue
		}
		if pathExists(mf.Dir, name) {
			out = append(out, Finding{
				Rule:     RulePhonyShouldBeFile,
				Severity: SeverityInfo,
				Target:   name,
				File:     primaryFile(mf),
				Message: fmt.Sprintf(
					"%q is declared .PHONY, but a file or directory named %q already exists; if this target is meant to produce that path, .PHONY forces make to rebuild it on every run instead of treating it as up to date",
					name, name),
			})
		}
	}

	for i := range mf.Rules {
		r := mf.Rules[i]
		if r.IsPattern || !r.HasRecipe || len(r.Targets) == 0 {
			continue
		}
		for _, t := range r.Targets {
			if !conventionalPhonyNames[t] {
				continue
			}
			if mf.PhonyNames[t] || r.PhonyByDB || shadowed[t] {
				continue
			}
			out = append(out, Finding{
				Rule:     RuleLikelyPhony,
				Severity: SeverityWarning,
				Target:   t,
				File:     r.File,
				Line:     r.Line,
				Message: fmt.Sprintf(
					"%q looks like a conventional phony target (no file named %q currently exists) but is not declared .PHONY; adding a file or directory with that name later will silently defeat it",
					t, t),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return out
}

func primaryFile(mf *mkparse.Makefile) string {
	if len(mf.Files) > 0 {
		return mf.Files[0]
	}
	return ""
}

func pathExists(dir, name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "..."
	}
	s := ""
	for i, n := range names {
		if i > 0 {
			s += " "
		}
		s += n
	}
	return s
}
