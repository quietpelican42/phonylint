package mkparse

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ParseDatabase asks GNU Make itself for its parsed rule database, via
// `make -pn`, and extracts the real, fully-resolved target list: after
// variables are expanded, conditionals are evaluated, and every include
// is processed.
//
// This is strictly more accurate than the static parser for the
// filesystem-shadow, phony-without-rule, and likely-phony checks, which
// all depend on knowing the *real* set of targets and which of them make
// itself considers phony (make prints "Phony target (prerequisite of
// .PHONY)" for each one it recognizes as such).
//
// It is not used for the .PHONY-typo / misspelling checks: those are
// about mistakes in the source text itself (a stray `=` where a `:`
// belonged), which the static parser already catches directly, and which
// `make -pn`'s output cannot distinguish (a typo'd `.PHONY = ...` is,
// correctly, just an ordinary variable to make, and never appears in the
// database as anything phony-related).
//
// IMPORTANT: `-p` evaluates the Makefile enough to run any $(shell ...),
// $(wildcard ...) or similar function calls it contains, exactly as a
// real `make` invocation would. Only call this against Makefiles you
// trust; ParseStatic never executes anything.
func ParseDatabase(ctx context.Context, dir string, files []string) (*Makefile, error) {
	args := []string{"-pn"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	// -f/PHONY etc. reference an unbuildable dummy goal so make doesn't
	// try to chase the real default goal (which may have its own
	// prerequisites); we still want the database, and -n means no recipe
	// runs regardless.
	cmd := exec.CommandContext(ctx, "make", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("make -pn timed out: %w", ctx.Err())
	}
	if err != nil {
		// A nonzero exit is common (make -pn still tries the default
		// goal in -n mode and some Makefiles fail before recipes run,
		// e.g. missing prerequisites); the database is printed to
		// stdout regardless, so only fail if we got nothing usable.
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("make -pn: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	mf := newMakefile(dir)
	mf.Files = append(mf.Files, files...)
	parseDatabaseOutput(mf, stdout.String())
	return mf, nil
}

func parseDatabaseOutput(mf *Makefile, out string) {
	lines := strings.Split(out, "\n")
	inFiles := false
	notATarget := false

	var cur *Rule
	finalize := func() {
		if cur == nil {
			return
		}
		if len(cur.Targets) == 1 && cur.Targets[0] == ".PHONY" {
			for _, n := range cur.NormalPrereqs {
				mf.PhonyNames[n] = true
			}
			cur = nil
			return
		}
		mf.Rules = append(mf.Rules, *cur)
		cur = nil
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")

		if line == "# Files" {
			inFiles = true
			continue
		}
		if !inFiles {
			continue
		}
		if line == "# files hash-table stats:" || line == "# Finished Make data base on this data." {
			finalize()
			inFiles = false
			continue
		}

		if line == "" {
			finalize()
			notATarget = false
			continue
		}
		if line == "# Not a target:" {
			finalize()
			notATarget = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			if cur != nil && strings.Contains(line, "Phony target") {
				cur.PhonyByDB = true
			}
			if cur != nil {
				if f, ln, ok := parseFromClause(line); ok {
					cur.File = f
					cur.Line = ln
				}
			}
			continue
		}
		if strings.HasPrefix(raw, "\t") {
			if cur != nil {
				cur.HasRecipe = true
				cur.RecipeText = append(cur.RecipeText, strings.TrimPrefix(raw, "\t"))
			}
			continue
		}

		// A new header line: "target: prereqs" or "target:: prereqs".
		finalize()
		if notATarget {
			notATarget = false
			continue
		}
		target, rest, double, ok := splitDBHeader(line)
		if !ok {
			continue
		}
		normal, order := splitOrderOnly(rest)
		r := Rule{
			Targets:       []string{target},
			NormalPrereqs: normal,
			OrderPrereqs:  order,
			IsDoubleColon: double,
			IsPattern:     strings.Contains(target, "%"),
		}
		cur = &r
	}
	finalize()
}

// splitDBHeader parses one "# Files" section header line, which GNU
// Make always prints as a single target (multi-target rule headers are
// expanded to one entry per target by the time -p prints them).
func splitDBHeader(line string) (target, rest string, double, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", "", false, false
	}
	target = line[:idx]
	if target == "" {
		return "", "", false, false
	}
	rest = line[idx+1:]
	if strings.HasPrefix(rest, ":") {
		double = true
		rest = rest[1:]
	}
	return target, strings.TrimSpace(rest), double, true
}

// parseFromClause extracts the file and line number from a comment like
// "#  commands to execute (from `Makefile', line 7):".
func parseFromClause(line string) (file string, lineNo int, ok bool) {
	const marker = "(from `"
	i := strings.Index(line, marker)
	if i < 0 {
		return "", 0, false
	}
	rest := line[i+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		return "", 0, false
	}
	file = rest[:end]
	rest = rest[end:]
	li := strings.Index(rest, "line ")
	if li < 0 {
		return file, 0, true
	}
	rest = rest[li+len("line "):]
	numEnd := strings.IndexAny(rest, "):")
	if numEnd < 0 {
		numEnd = len(rest)
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:numEnd]))
	if err != nil {
		return file, 0, true
	}
	return file, n, true
}
