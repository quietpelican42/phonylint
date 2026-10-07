// Command phonylint finds Makefile targets that GNU Make will silently
// skip because they are not really phony and a file or directory with
// the same name already exists, plus related .PHONY mistakes (typo'd
// assignments, misspellings, names in .PHONY with no matching rule).
//
// Exit codes: 0 clean, 1 findings reported, 2 could not analyse.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/quietpelican42/phonylint/internal/mkparse"
	"github.com/quietpelican42/phonylint/internal/report"
	"github.com/quietpelican42/phonylint/internal/rules"
)

type fileFlags []string

func (f *fileFlags) String() string { return fmt.Sprint([]string(*f)) }
func (f *fileFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("phonylint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var files fileFlags
	fs.Var(&files, "f", "Makefile to read (repeatable); default is GNUmakefile, makefile, or Makefile in -C")
	dir := fs.String("C", ".", "directory to analyse (like make -C)")
	format := fs.String("format", "text", "output format: text, json, github")
	execMode := fs.Bool("exec", false, "use `make -pn` for the authoritative rule set (executes $(shell ...) in the Makefile; only use on trusted repos)")
	timeout := fs.Duration("timeout", 10*time.Second, "timeout for invoking make when -exec is set")
	version := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: phonylint [-C dir] [-f Makefile]... [-exec] [-format text|json|github] [dirs...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, "phonylint dev")
		return 0
	}

	targets := fs.Args()
	if len(targets) == 0 {
		targets = []string{*dir}
	}

	var all []rules.Finding
	exitCode := 0
	for _, t := range targets {
		findings, warnings, err := analyzeOne(t, files, *execMode, *timeout)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "phonylint: %s: %s\n", t, w)
		}
		if err != nil {
			fmt.Fprintf(stderr, "phonylint: %s: %v\n", t, err)
			if exitCode < 2 {
				exitCode = 2
			}
			continue
		}
		all = append(all, findings...)
	}

	if err := report.Write(stdout, all, *format); err != nil {
		fmt.Fprintf(stderr, "phonylint: %v\n", err)
		return 2
	}

	if exitCode == 2 {
		return 2
	}
	for _, f := range all {
		if f.Severity == rules.SeverityError || f.Severity == rules.SeverityWarning {
			return 1
		}
	}
	return 0
}

func analyzeOne(dirArg string, files fileFlags, execMode bool, timeout time.Duration) ([]rules.Finding, []string, error) {
	dir, err := filepath.Abs(dirArg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %s: %w", dirArg, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", dirArg)
	}

	mf, err := mkparse.ParseStatic(dir, []string(files))
	if err != nil {
		return nil, nil, err
	}

	var warnings []string
	warnings = append(warnings, mf.Warnings...)

	if execMode {
		if _, lookErr := lookMake(); lookErr != nil {
			warnings = append(warnings, "make not found on PATH; falling back to the static parser (pass -exec only when make is installed)")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			dbFiles := files
			if len(dbFiles) == 0 {
				def, ferr := mkparse.FindDefaultMakefile(dir)
				if ferr == nil {
					dbFiles = fileFlags{def}
				}
			}
			dbMF, dbErr := mkparse.ParseDatabase(ctx, dir, []string(dbFiles))
			cancel()
			if dbErr != nil {
				warnings = append(warnings, fmt.Sprintf("make -pn failed, falling back to the static parser: %v", dbErr))
			} else {
				// Use the database engine's authoritative rule set and
				// phony status, but keep the static parser's source-text
				// findings (typo'd/misspelled .PHONY forms), which -pn's
				// output cannot represent.
				dbMF.Specials = mf.Specials
				dbMF.Vars = mf.Vars
				for k := range mf.PhonyNames {
					dbMF.PhonyNames[k] = true
				}
				warnings = append(warnings, dbMF.Warnings...)
				mf = dbMF
			}
		}
	}

	findings := rules.Analyze(mf)
	if cwd, err := os.Getwd(); err == nil {
		for i := range findings {
			if rel, err := filepath.Rel(cwd, findings[i].File); err == nil && !strings.HasPrefix(rel, "..") {
				findings[i].File = rel
			}
		}
	}
	return findings, warnings, nil
}

func lookMake() (string, error) {
	return exec.LookPath("make")
}
