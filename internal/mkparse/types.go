// Package mkparse builds a normalized view of a Makefile's rules and
// .PHONY declarations, either by parsing the source text directly or by
// parsing the database that GNU Make itself prints with -p.
package mkparse

// Rule is one target definition: either a normal rule (`target: prereqs`)
// or a pattern rule (`%.o: %.c`). Special targets such as .PHONY are not
// represented as Rules; they are collected separately.
type Rule struct {
	// Targets lists every name this rule header defines (a rule can list
	// more than one target before the colon).
	Targets []string
	// NormalPrereqs are prerequisites that participate in the "is this
	// target out of date" timestamp comparison.
	NormalPrereqs []string
	// OrderPrereqs are prerequisites after a `|` separator: make ensures
	// they exist but never rebuilds the target because of their timestamp.
	OrderPrereqs []string
	// HasRecipe is true if the rule has at least one recipe line (a
	// tab-indented command, or an inline `;` command).
	HasRecipe bool
	// RecipeText holds the raw recipe lines, used only by the info-level
	// "should this be a file target" heuristic.
	RecipeText []string
	// File is the Makefile (or included file) this rule came from.
	File string
	// Line is the 1-based line number of the rule header.
	Line int
	// IsPattern is true if any target contains a `%` (a pattern rule);
	// pattern rules are never checked against the filesystem.
	IsPattern bool
	// IsDoubleColon is true for a `target:: prereqs` rule.
	IsDoubleColon bool
	// PhonyByDB is only set by the -pn database engine: GNU Make itself
	// annotated this target "Phony target (prerequisite of .PHONY)".
	// It is authoritative when present.
	PhonyByDB bool
}

// VarAssign is a variable (or macro) assignment line.
type VarAssign struct {
	Name string
	Op   string // "=", ":=", "::=", "?=", "+=", "!="
	File string
	Line int
}

// SpecialTarget is an occurrence of a dot-prefixed special-target-like
// rule header, e.g. `.PHONY:` or a typo of it such as `.PHONEY:` or
// `.PHONY=`. Kept distinct from Rule so the phony-specific checks can
// reason about exactly how it was spelled and introduced.
type SpecialTarget struct {
	Name  string   // as written, e.g. ".PHONY", ".phony", ".PHONY" (var)
	Names []string // the prerequisite/value names listed after it
	AsVar bool     // true if this came from a variable assignment (`=`, `:=`, ...) rather than a rule (`:`)
	Op    string   // the operator used ("=", ":=", "+=", ":" for a real rule, "PHONY:" for missing-dot)
	File  string
	Line  int
}

// Makefile is the normalized result of parsing one Makefile and every
// file it includes.
type Makefile struct {
	// Dir is the directory the primary Makefile lives in. All filesystem
	// checks (does a path with this target's name exist) are relative to
	// this directory, matching where `make` itself would resolve them.
	Dir string
	// Files lists every file that was read, in read order (primary file
	// first, then each include as it was encountered).
	Files []string
	// Rules is every ordinary rule, across the primary file and includes.
	Rules []Rule
	// Specials is every dot-prefixed special-target-like header seen,
	// correctly spelled or not, plus PHONY-without-a-dot rules.
	Specials []SpecialTarget
	// Vars is every variable assignment seen (used to catch `.PHONY = ...`
	// typo'd as a variable).
	Vars []VarAssign
	// PhonyNames is the set of names declared phony via a correctly
	// spelled, correctly formed `.PHONY: name...` rule (colon form).
	PhonyNames map[string]bool
	// Warnings holds non-fatal parser notes (unresolved $(VAR) in an
	// include, a skipped conditional branch, recursion limits, etc.) that
	// are surfaced to the user but do not stop analysis.
	Warnings []string
}

func newMakefile(dir string) *Makefile {
	return &Makefile{
		Dir:        dir,
		PhonyNames: map[string]bool{},
	}
}
