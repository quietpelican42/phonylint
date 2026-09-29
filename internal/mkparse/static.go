package mkparse

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gnuSpecialTargets are the dot-prefixed targets GNU Make treats
// specially. None of them are file-shadow or likely-phony candidates.
var gnuSpecialTargets = map[string]bool{
	".PHONY":                true,
	".SUFFIXES":             true,
	".DEFAULT":              true,
	".PRECIOUS":             true,
	".INTERMEDIATE":         true,
	".SECONDARY":            true,
	".SECONDEXPANSION":      true,
	".DELETE_ON_ERROR":      true,
	".IGNORE":               true,
	".LOW_RESOLUTION_TIME":  true,
	".SILENT":               true,
	".EXPORT_ALL_VARIABLES": true,
	".NOTPARALLEL":          true,
	".ONESHELL":             true,
	".POSIX":                true,
}

// DefaultMakefileNames is the lookup order GNU Make itself uses when no
// -f is given.
var DefaultMakefileNames = []string{"GNUmakefile", "makefile", "Makefile"}

// FindDefaultMakefile looks for GNUmakefile, makefile, then Makefile (in
// that order) inside dir, and returns the first one found.
func FindDefaultMakefile(dir string) (string, error) {
	// Read the directory once and match against its real entries. A
	// plain os.Stat per candidate would still find the right file on a
	// case-insensitive filesystem (macOS, Windows), but would then
	// report it back under the candidate's spelling rather than its
	// real on-disk name, which is confusing in output.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	exact := make(map[string]bool, len(entries))
	folded := make(map[string]string, len(entries)) // lowercase -> real name
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		exact[e.Name()] = true
		lower := strings.ToLower(e.Name())
		if _, ok := folded[lower]; !ok {
			folded[lower] = e.Name()
		}
	}
	for _, name := range DefaultMakefileNames {
		if exact[name] {
			return filepath.Join(dir, name), nil
		}
		if real, ok := folded[strings.ToLower(name)]; ok {
			return filepath.Join(dir, real), nil
		}
	}
	return "", fmt.Errorf("no GNUmakefile, makefile, or Makefile in %s", dir)
}

const maxIncludeDepth = 20

// ParseStatic parses files (the -f arguments, in order; if empty, the
// default lookup is used against dir) without invoking make or expanding
// any $(shell ...), $(wildcard ...), or variable reference. It is the
// default, safe engine: nothing in the Makefile is executed.
func ParseStatic(dir string, files []string) (*Makefile, error) {
	if len(files) == 0 {
		f, err := FindDefaultMakefile(dir)
		if err != nil {
			return nil, err
		}
		files = []string{f}
	}
	mf := newMakefile(dir)
	visited := map[string]bool{}
	for _, f := range files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, f)
		}
		if err := parseFile(mf, abs, visited, 0); err != nil {
			return nil, err
		}
	}
	// Fold real `.PHONY: ...` rule occurrences into the PhonyNames set.
	for _, s := range mf.Specials {
		if s.Name == ".PHONY" && !s.AsVar && s.Op == ":" {
			for _, n := range s.Names {
				mf.PhonyNames[n] = true
			}
		}
	}
	return mf, nil
}

type parserState struct {
	mf   *Makefile
	file string
	cur  *Rule // rule currently accumulating recipe lines, or nil
}

func parseFile(mf *Makefile, path string, visited map[string]bool, depth int) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	if visited[absPath] {
		mf.Warnings = append(mf.Warnings, fmt.Sprintf("skipped %s: already included (cycle)", path))
		return nil
	}
	if depth > maxIncludeDepth {
		mf.Warnings = append(mf.Warnings, fmt.Sprintf("skipped %s: include depth limit (%d) exceeded", path, maxIncludeDepth))
		return nil
	}
	visited[absPath] = true

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	mf.Files = append(mf.Files, path)

	rawLines, err := readLogicalLines(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	st := &parserState{mf: mf, file: path}
	inDefine := false
	defineDepth := 0

	for _, ll := range rawLines {
		line := ll.text

		if inDefine {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "define ") || trimmed == "define" {
				defineDepth++
			} else if trimmed == "endef" || strings.HasPrefix(trimmed, "endef ") {
				if defineDepth == 0 {
					inDefine = false
				} else {
					defineDepth--
				}
			}
			continue
		}

		// Recipe continuation: a raw tab-indented line immediately
		// following an open rule.
		if ll.tabIndented && st.cur != nil {
			st.cur.HasRecipe = true
			st.cur.RecipeText = append(st.cur.RecipeText, line)
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			st.cur = nil
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// GNU Make strips a trailing, unescaped `#` comment from any
		// non-recipe line (a rule header, a variable assignment, a
		// directive) before parsing it further. `\#` is a literal `#`.
		trimmed = strings.TrimSpace(stripTrailingComment(trimmed))
		if trimmed == "" {
			st.cur = nil
			continue
		}

		// Directives.
		if word, rest, ok := directiveWord(trimmed); ok {
			switch word {
			case "include", "-include", "sinclude":
				st.cur = nil
				handleInclude(mf, rest, filepath.Dir(path), visited, depth)
				continue
			case "ifeq", "ifneq", "ifdef", "ifndef", "else", "endif":
				// Static mode does not evaluate conditionals: every
				// branch is parsed as if active. This can over-report
				// (e.g. counting a target only defined on an OS this
				// isn't) but never silently misses a real rule.
				st.cur = nil
				continue
			case "define":
				inDefine = true
				defineDepth = 0
				st.cur = nil
				continue
			case "export", "override", "unexport", "vpath":
				// Fall through to normal parsing for `export FOO = bar`
				// (still a variable assignment); `export` / `unexport`
				// alone, and `vpath`, are directives we don't model.
				if word == "export" || word == "override" {
					trimmed = rest
					if trimmed == "" {
						st.cur = nil
						continue
					}
					break
				}
				st.cur = nil
				continue
			}
		}

		kind, lhs, op, rhs := splitDirective(trimmed)
		switch kind {
		case kindVar:
			handleVarLine(st, lhs, op, rhs, ll.line)
		case kindRule:
			handleRuleLine(st, lhs, op, rhs, ll.line)
		default:
			// Unparseable line (e.g. a bare word). Ignore.
			st.cur = nil
		}
	}
	return nil
}

func handleInclude(mf *Makefile, rest, baseDir string, visited map[string]bool, depth int) {
	for _, tok := range strings.Fields(rest) {
		if strings.Contains(tok, "$") {
			mf.Warnings = append(mf.Warnings, fmt.Sprintf("include %q not followed: contains an unexpanded variable (static mode does not expand variables)", tok))
			continue
		}
		p := tok
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		if _, err := os.Stat(p); err != nil {
			mf.Warnings = append(mf.Warnings, fmt.Sprintf("include %q not found (skipped)", tok))
			continue
		}
		if err := parseFile(mf, p, visited, depth+1); err != nil {
			mf.Warnings = append(mf.Warnings, fmt.Sprintf("include %q: %v", tok, err))
		}
	}
}

func handleVarLine(st *parserState, lhs, op, rhs string, line int) {
	_ = rhs
	name := strings.TrimSpace(lhs)
	st.cur = nil

	if isPhonyLikeName(name) {
		st.mf.Specials = append(st.mf.Specials, SpecialTarget{
			Name:  name,
			Names: strings.Fields(rhs),
			AsVar: true,
			Op:    op,
			File:  st.file,
			Line:  line,
		})
		return
	}
	st.mf.Vars = append(st.mf.Vars, VarAssign{Name: name, Op: op, File: st.file, Line: line})
}

func handleRuleLine(st *parserState, lhs, op, rhs string, line int) {
	targets := strings.Fields(lhs)
	if len(targets) == 0 {
		st.cur = nil
		return
	}

	// A bare "PHONY:" (missing the dot) rule header.
	if len(targets) == 1 && strings.EqualFold(targets[0], "PHONY") && targets[0] != ".PHONY" {
		prereqPart, inline := splitInlineRecipe(rhs)
		st.mf.Specials = append(st.mf.Specials, SpecialTarget{
			Name:  targets[0],
			Names: strings.Fields(prereqPart),
			AsVar: false,
			Op:    "PHONY:",
			File:  st.file,
			Line:  line,
		})
		st.cur = nil
		if inline != "" {
			// Rare, but keep the recipe out of "no rule for PHONY".
		}
		return
	}

	// A dot-prefixed header that is, or looks like, .PHONY.
	if len(targets) == 1 && strings.HasPrefix(targets[0], ".") && isPhonyLikeName(targets[0]) {
		prereqPart, _ := splitInlineRecipe(rhs)
		if op == "::" {
			prereqPart = strings.TrimPrefix(rhs, ":")
		}
		st.mf.Specials = append(st.mf.Specials, SpecialTarget{
			Name:  targets[0],
			Names: strings.Fields(prereqPart),
			AsVar: false,
			Op:    ":",
			File:  st.file,
			Line:  line,
		})
		st.cur = nil
		return
	}

	// Any other GNU special target (.SUFFIXES, .DEFAULT, ...): record but
	// don't treat as a shadow/likely-phony candidate, and don't open a
	// recipe-accumulating rule for it (irrelevant to every check here).
	if len(targets) == 1 && gnuSpecialTargets[targets[0]] {
		st.cur = nil
		return
	}

	prereqPart, inlineRecipe := splitInlineRecipe(rhs)
	normal, order := splitOrderOnly(prereqPart)

	r := Rule{
		Targets:       targets,
		NormalPrereqs: normal,
		OrderPrereqs:  order,
		File:          st.file,
		Line:          line,
		IsDoubleColon: op == "::",
	}
	for _, t := range targets {
		if strings.Contains(t, "%") {
			r.IsPattern = true
		}
	}
	if inlineRecipe != "" {
		r.HasRecipe = true
		r.RecipeText = append(r.RecipeText, inlineRecipe)
	}
	st.mf.Rules = append(st.mf.Rules, r)
	st.cur = &st.mf.Rules[len(st.mf.Rules)-1]
}

// isPhonyLikeName reports whether name, uppercased and with any leading
// dot stripped, is either exactly PHONY or close enough (edit distance
// <= 2, similar length) to be a plausible misspelling of it. It is used
// both for the correctly-spelled ".PHONY" case and for typo detection.
func isPhonyLikeName(name string) bool {
	n := strings.TrimPrefix(name, ".")
	u := strings.ToUpper(n)
	if u == "PHONY" {
		return true
	}
	if len(u) < 3 || len(u) > 8 {
		return false
	}
	return levenshtein(u, "PHONY") <= 2
}

func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

// stripTrailingComment removes a `#` comment and everything after it
// from a non-recipe line, the way GNU Make does: `\#` is a literal `#`
// and is not a comment start.
func stripTrailingComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '#' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			continue // escaped `#`, not a comment start
		}
		return s[:i]
	}
	return s
}

func splitInlineRecipe(rhs string) (prereqs, recipe string) {
	depth := 0
	for i := 0; i < len(rhs); i++ {
		switch rhs[i] {
		case '(', '{':
			depth++
		case ')', '}':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				return strings.TrimSpace(rhs[:i]), strings.TrimSpace(rhs[i+1:])
			}
		}
	}
	return strings.TrimSpace(rhs), ""
}

func splitOrderOnly(s string) (normal, order []string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '{':
			depth++
		case ')', '}':
			if depth > 0 {
				depth--
			}
		case '|':
			if depth == 0 {
				return strings.Fields(s[:i]), strings.Fields(s[i+1:])
			}
		}
	}
	return strings.Fields(s), nil
}

type splitKind int

const (
	kindNone splitKind = iota
	kindVar
	kindRule
)

// splitDirective classifies one logical (continuation-joined, comment
// and leading-whitespace stripped) line as a variable assignment or a
// rule header, and returns the pieces either way. It scans left to right
// at paren/brace depth 0, exactly the way GNU Make disambiguates `:` and
// `=`: whichever appears first wins.
func splitDirective(line string) (kind splitKind, lhs, op, rhs string) {
	depth := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '(', '{':
			depth++
			continue
		case ')', '}':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 {
			continue
		}
		switch c {
		case ':':
			if i+2 <= len(line) && strings.HasPrefix(line[i:], "::=") {
				return kindVar, line[:i], "::=", line[i+3:]
			}
			if i+1 < len(line) && line[i+1] == '=' {
				return kindVar, line[:i], ":=", line[i+2:]
			}
			if i+1 < len(line) && line[i+1] == ':' {
				return kindRule, line[:i], "::", line[i+2:]
			}
			return kindRule, line[:i], ":", line[i+1:]
		case '?':
			if i+1 < len(line) && line[i+1] == '=' {
				return kindVar, line[:i], "?=", line[i+2:]
			}
		case '+':
			if i+1 < len(line) && line[i+1] == '=' {
				return kindVar, line[:i], "+=", line[i+2:]
			}
		case '!':
			if i+1 < len(line) && line[i+1] == '=' {
				return kindVar, line[:i], "!=", line[i+2:]
			}
		case '=':
			return kindVar, line[:i], "=", line[i+1:]
		}
	}
	return kindNone, line, "", ""
}

// directiveWord reports the first word of trimmed if it is one of the
// GNU Make directive keywords, plus the remainder of the line.
func directiveWord(trimmed string) (word, rest string, ok bool) {
	fields := strings.SplitN(trimmed, " ", 2)
	w := fields[0]
	switch w {
	case "include", "-include", "sinclude",
		"ifeq", "ifneq", "ifdef", "ifndef", "else", "endif",
		"define", "endef",
		"export", "override", "unexport", "vpath":
		r := ""
		if len(fields) == 2 {
			r = strings.TrimSpace(fields[1])
		}
		return w, r, true
	}
	return "", "", false
}

type logicalLine struct {
	text        string // continuation-joined, backslash-newlines collapsed to single spaces
	line        int    // line number of the first physical line
	tabIndented bool   // true if the *first* physical line started with a tab (a recipe line)
}

// readLogicalLines reads f and joins backslash-continued physical lines
// into logical lines, the way make does before any further parsing.
func readLogicalLines(f *os.File) ([]logicalLine, error) {
	var out []logicalLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	var buf strings.Builder
	startLine := 0
	tabIndented := false
	inCont := false
	flush := func() {
		out = append(out, logicalLine{text: buf.String(), line: startLine, tabIndented: tabIndented})
		buf.Reset()
		inCont = false
	}
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		if !inCont {
			startLine = lineNo
			tabIndented = strings.HasPrefix(raw, "\t")
		}
		trimmedForCont := strings.TrimRight(raw, "\r")
		if strings.HasSuffix(trimmedForCont, "\\") {
			buf.WriteString(strings.TrimSuffix(trimmedForCont, "\\"))
			buf.WriteString(" ")
			inCont = true
			continue
		}
		buf.WriteString(trimmedForCont)
		flush()
	}
	if inCont {
		flush()
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
