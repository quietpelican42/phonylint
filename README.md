# phonylint

Finds Makefile targets that GNU Make will silently skip because they are not
really phony and a file or directory with the same name already exists.

```sh
$ make docs
make: `docs' is up to date.

$ phonylint
Makefile:1: error: [phony-assignment-typo] `.PHONY = ...` assigns a variable named ".PHONY"; it does not declare anything phony. Use `.PHONY: all clean docs`
Makefile:5: error: [phony-shadowed-by-path] target "docs" has a recipe but is not declared .PHONY, and a file or directory named "docs" exists next to the Makefile: `make docs` will print "docs is up to date" and skip the recipe, silently and every time

2 finding(s): 2 error, 0 warning, 0 info
```

## Why

A target with a recipe, no real prerequisites, and no `.PHONY` declaration is
indistinguishable from a file target once something on disk shares its name.
Make then compares the target's mtime to nothing, decides it is already
up to date, prints `'name' is up to date.`, and exits 0 without ever running
the recipe — every time, permanently, until the path is renamed or removed.
The common way this happens is a `.PHONY` typo: `.PHONY=all clean docs`
(`=` instead of `:`) defines a variable named `.PHONY` and marks nothing
phony at all.

Measured: `grimm-co/killerbeez`'s real `Makefile` has exactly this typo, and
its `docs/` directory defeats the `docs` target:

```sh
$ cat Makefile
.PHONY=all clean docs

all: docs

docs:
	# Defer to the Makefile in the docs directory
	make -C docs

$ make docs
make: `docs' is up to date.
```
(reproduced locally with the file fetched from the repository; GNU Make 3.81,
macOS)

Checked for coverage first: **checkmake** 0.2.2's `phonydeclared` rule only
flags a target with an *empty* recipe that isn't `.PHONY` — the opposite
direction, and it has no filesystem awareness at all, so it cannot see that
`docs` collides with a real path. **mbake**'s "smart phony detection"
(`mbake/core/rules/phony.py`) is a heuristic over recipe text (does the
command look like `npm test`, does it write to `$@`); it never calls
`os.path.exists` either. Neither tool would have caught the killerbeez case.

Scanned 11 real-world Makefiles this way (cli/cli, docker/compose,
etcd-io/bbolt, grafana/grafana, junegunn/fzf, moby/moby,
prometheus/prometheus, redis/redis, spf13/cobra, containerd/containerd,
git/git) in the default static mode: 0 false positives after two parser bugs
found and fixed during the scan (see Limitations), 1 confirmed real
"is up to date" bug in grafana/grafana's `Makefile` (a generated,
already-checked-in file with no prerequisites and no `.PHONY`), and 4
confirmed real dead/missing `.PHONY` entries or missing declarations
(docker/compose's `help`, etcd-io/bbolt's `build`, containerd's
`all-coverage` / `integration-coverage`). The rest were `info`-level notes on
targets like `test` or `docs` that coincide with an unrelated directory of
the same name and are correctly phony regardless.

## Installation

```sh
go install github.com/__GH_OWNER__/phonylint/cmd/phonylint@latest
```

Or build from source: `go build ./cmd/phonylint`. Requires Go 1.23+. No
runtime dependencies — stdlib only.

## Usage

### Lint the Makefile in the current directory

```sh
phonylint
```

### Lint a specific directory or Makefile

```sh
phonylint -C path/to/project
phonylint -f build/Makefile -f build/common.mk
```

### Use GNU Make's own parsed rule set instead of the static parser

```sh
phonylint -exec
```

`-exec` runs `make -pn` and uses its authoritative, fully-resolved rule
database (after variables, `include`s, and `ifeq`/`ifdef` conditionals are
evaluated) instead of parsing the source text. It is **not** the default:
`make -p` evaluates every `$(shell ...)` and `$(wildcard ...)` call in the
Makefile exactly as a real build would, which is unsafe on a Makefile you
don't trust. The static parser never executes anything and is the default
for that reason; use `-exec` only on Makefiles you already run yourself.

### In CI

```sh
phonylint -format github
```

Emits `::error`/`::warning`/`::notice` workflow commands so findings appear
as inline annotations on the diff.

## Reference

| Flag | Default | Description |
| --- | --- | --- |
| `-C` | `.` | Directory to analyze (like `make -C`). |
| `-f` | *(auto)* | Makefile to read; repeatable. Default is `GNUmakefile`, `makefile`, then `Makefile`, whichever exists first — GNU Make's own lookup order. |
| `-exec` | `false` | Use `make -pn` for the rule set instead of the static parser. Executes `$(shell ...)`; see Usage. |
| `-timeout` | `10s` | Timeout for the `make -pn` invocation when `-exec` is set. |
| `-format` | `text` | `text`, `json`, or `github`. |
| `-version` | | Print the version and exit. |

Positional arguments are directories to analyze instead of `-C` (useful for
linting several Makefiles in one run); if none are given, `-C`'s directory is
used.

### Rules

| Rule | Severity | Reports |
| --- | --- | --- |
| `phony-shadowed-by-path` | error | A non-phony target with a recipe and no real (non order-only) prerequisites, whose name matches an existing file or directory: `make` will report it up to date and skip the recipe, permanently. |
| `phony-assignment-typo` | error | `.PHONY =`, `.PHONY :=`, `.PHONY +=`, or `PHONY:` used where `.PHONY:` was meant — a variable assignment or a wrongly-named ordinary target, not the special target. |
| `phony-misspelled` | error | `.phony:`, `.PHONEY:`, and similar near-misses of `.PHONY:` that GNU Make does not recognize. |
| `phony-without-rule` | warning | `.PHONY` lists a name with no matching rule (pattern rules count; the `FORCE` idiom — `.PHONY: FORCE` with deliberately no `FORCE:` rule — is exempted). |
| `likely-phony` | warning | A conventional non-file target name (`all`, `clean`, `test`, `lint`, `fmt`, `install`, `build`, `run`, `docs`, `help`, `check`, `release`, `deps`) with a recipe, not declared `.PHONY`, that doesn't currently collide with a path but would silently break if one appeared. |
| `phony-should-be-file` | info | The reverse case: a target *is* declared `.PHONY` and a file or directory of that name already exists, which forces an unconditional rebuild every run. Restricted to non-conventional names, since a real directory sharing a name with `test`/`docs`/`install`/etc. is normal and not a sign of a mistake. |

Exit codes: `0` no error/warning findings (info findings may still be
printed), `1` at least one error or warning finding, `2` could not analyze
(no Makefile found, unreadable file, `make -pn` failed with no usable
output).

## How it works

By default, phonylint reads the Makefile itself: it joins backslash
continuations, strips `#` comments (respecting `\#`), follows `include`s
relative to the Makefile's directory, and classifies each logical line as a
variable assignment or a rule header by scanning left to right at
paren/brace depth 0 for the first `:`, `:=`, `::=`, `?=`, `+=`, or `!=` —
whichever comes first wins, the same way GNU Make itself disambiguates
`.PHONY = x` from `.PHONY: x`. Prerequisites are split on `|` into normal and
order-only. Every rule's targets are then checked against the filesystem,
relative to the Makefile's own directory.

With `-exec`, phonylint instead asks GNU Make for its own database
(`make -pn`) and reads the `# Files` section: each target's real,
fully-resolved prerequisite list, and whether make itself printed
`Phony target (prerequisite of .PHONY)` for it — which is authoritative,
unlike anything a static parser can infer. The `.PHONY`-typo checks still run
against the source text in both modes, since a typo'd `.PHONY = ...` is, to
`make -pn`, just an ordinary variable; it never shows up as anything
phony-related in the database.

`phony-shadowed-by-path` only fires when a target has zero *normal*
prerequisites (order-only prerequisites, or none at all). Verified directly
against GNU Make 3.81: a normal prerequisite that is itself phony forces the
dependent to rebuild on every invocation (phony targets are always
considered out of date, and that propagates), so it is never silently
skipped — the earlier hypothesis that a phony *normal* prerequisite would
still allow the skip does not hold and was dropped. An order-only phony
prerequisite has no such effect and does not protect the target.

## Limitations

- The static parser does not evaluate `ifeq`/`ifdef`/`ifndef` conditionals;
  it parses every branch as if active. This can over-report a target that
  only exists on some platform or configuration, but it will never miss a
  real one. `-exec` resolves conditionals correctly by asking make directly.
- The static parser does not expand `$(VAR)` in `include` directives (an
  `include $(SOME_GENERATED_FILE)` is skipped with a warning, not followed).
  `-exec` has no such limitation.
- Filesystem lookups (does a path with this target's name exist) are exact
  string matches against directory entries at the time phonylint runs; a
  case-insensitive filesystem (macOS, Windows) can make `GNUmakefile`,
  `makefile`, and `Makefile` ambiguous in the same way it is ambiguous for
  `make` itself. phonylint resolves this the same way: whichever real
  on-disk name matches first in that lookup order wins.
- `phony-shadowed-by-path` cannot know future timestamps: a target with a
  *normal* prerequisite that is a real (non-phony) file is left alone
  entirely, even though whether its recipe runs still depends on relative
  mtimes at build time. That case is `make` working as intended, not the bug
  this tool targets.
- No dependency-graph or recursive-make analysis: each Makefile (plus its
  `include`s) is checked independently.
- `phony-should-be-file` is a filesystem-existence heuristic, not proof that
  a target is meant to produce that exact path; it is deliberately `info`
  severity and does not affect the exit code.

## Compatibility

Developed and tested on macOS (Darwin 25), Go 1.27, against GNU Make 3.81
(the version macOS ships). `gmake` / a newer GNU Make was not available in
this environment to test against; the `-pn` output format used by the
database engine has been stable across GNU Make 3.8x–4.x, but only 3.81 was
actually run here. CI (`.github/workflows/test.yml`) runs on Ubuntu with
current GNU Make, but that is "run in CI," not something exercised by hand
during development.

## Development

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .   # must print nothing
```

End-to-end tests in `cmd/phonylint/e2e_test.go` build the real binary, run
real GNU Make against fixture Makefiles, and assert on `make`'s actual
output (the recipe not running, then running after the suggested fix) —
they skip loudly if `make` is not on `PATH`. Tests that use `-exec`
(`internal/mkparse/database_test.go`) do the same.

## License

MIT. See [LICENSE](LICENSE).
