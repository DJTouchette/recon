# Recon

Fast, deterministic repo intelligence. No AI, no network, no guessing — just static analysis that finishes in seconds.

Your AI coding agent spends a lot of time running raw `grep` and hoping it stumbles onto the right file. Recon replaces that with real answers: who imports this, what tests cover it, which files always change together, where the scary high-risk code lives. Every result is reproducible and requires nothing but the repo itself.

Recon is the codebase-intelligence layer of the [Rivet](https://github.com/djtouchette/rivet) ecosystem. It runs standalone as a CLI, and Rivet embeds it to feed its MCP tools and context system with deterministic facts.

## What It Does

- **Dependency graph** — import resolution and reverse lookups across 14 languages (`imports of` / `imported by`)
- **Symbol search** — functions, types, classes, methods, parsed from a real grammar (tree-sitter) where available, with regex fallback elsewhere
- **Call graph** — `callers` finds every site that references a symbol, resolved against its definitions via the import graph (tree-sitter, for Go, JS/TS, Python, C#, Java, Rust, Ruby, PHP, Lua, Shell, Julia, Zig)
- **Co-change history** — files that always change together, mined from git log
- **Hotspot detection** — high fan-in × high churn = the code that's risky to touch
- **Enriched grep** — classifies every match as a definition, reference, test, or comment
- **File context** — preview, fan-in/fan-out, churn, ownership (CODEOWNERS), nearby configs
- **Test mapping** — which tests cover which source files
- **Project overview** — languages, frameworks, entrypoints, structure

## Quick Start

```bash
# Install
go install github.com/djtouchette/recon/cmd/recon@latest

# From your project root
recon overview              # what is this project?
recon search "PaymentService"
recon related internal/orders/handler.go
recon hotspots --human      # what's risky to change?
```

The first run scans the repo and builds a cache; subsequent runs are incremental.

## Commands

All commands emit JSON by default (built to be consumed by tools). Add `--human` for readable output.

| Command | What it does |
|---------|-------------|
| `recon overview` | Project structure, languages, frameworks, entrypoints |
| `recon search <query>` | Unified search across symbols, paths, and content. Start here. |
| `recon grep <pattern>` | Enriched grep with definition/reference/test/comment classification (`--type`) |
| `recon related <path>` | Files related to a path (imports, co-change, naming, test pairs) |
| `recon symbols [query]` | Search or list functions, types, classes. `file:<path>` lists a file's symbols |
| `recon callers <name>` | Where a symbol is defined and every call site that references it |
| `recon context <path>` | File preview, fan-in/fan-out, churn, ownership, nearby configs |
| `recon hotspots` | Top files ranked by risk (fan-in × churn) |
| `recon tests <path>` | Find test files for a source file |
| `recon changes` | Recent git change summary (`--since 2w`) |
| `recon refresh` | Incremental cache update |
| `recon rebuild` | Full rescan from scratch |
| `recon version` | Version info |

### Global flags

| Flag | Description |
|------|-------------|
| `--root <path>` | Repo root (default: current directory) |
| `--cache-dir <path>` | Cache directory (default: `<root>/.recon/`) |
| `--human` | Human-readable output instead of JSON |
| `-n, --max <n>` | Limit number of results |

## Language Support

Symbol and import analysis covers **Go, JavaScript/TypeScript, Python, Java, Kotlin, C#, Ruby, Rust, PHP, Dart, Scala, Swift, Elixir** — with language-aware resolution (Go modules, JS relative paths + `node_modules`, Java/Kotlin namespaces, C# namespaces, PHP/Composer PSR-4, Dart packages, Swift SPM targets, and more). File classification recognizes 50+ extensions across source, test, config, generated, docs, and assets.

Symbol extraction uses **tree-sitter** grammars (real parsing — no false matches from strings or comments, accurate multi-line signatures) for **Go, Python, JavaScript, TypeScript, Rust, Ruby, Java, C#, PHP, Scala, Kotlin, C, C++, Lua, Shell/Bash, Julia, and Zig**, and falls back to fast regex patterns for **Swift, Dart, and Elixir** (whose grammars aren't usable as Go modules). Each grammar's symbol query lives in `internal/index/queries/<lang>.scm`, so adding or tuning a language is just editing a query file.

Import extraction uses **tree-sitter** for **JavaScript, TypeScript, Python, Go, Java, Kotlin, C#, PHP, Scala, Ruby, Rust, Lua, Julia, Zig, and Shell** (queries in `internal/index/queries/imports/`), which correctly handles multi-line imports, `export … from` re-exports, and never picks up imports hiding in comments or strings; the per-language resolution to local files (Go module paths, PSR-4, Ruby `require`/`require_relative`, Rust `use`/`mod` crate paths, Zig `@import`, etc.) is hand-written and unchanged.

## How It Works

```
recon scan
  → walk the tree (gitignore-aware), classify every file by language and role
  → build the dependency graph (imports / imported-by)
  → extract symbols, map tests to sources, parse CODEOWNERS
  → mine git log for churn and co-change
  → compute fan-in / fan-out / hotspot scores
  → persist everything to .recon/recon.db (SQLite)
```

Subsequent runs check the git HEAD and key manifest files (`go.mod`, `package.json`, `Cargo.toml`, …). If nothing relevant changed, results come straight from cache; otherwise only the changed files are re-parsed.

The compiled binary, the `bin/` dir, and the `.recon/` cache are all gitignored — nothing recon produces needs to be committed.

## Library Use

Recon's analysis is available as a Go package (`github.com/djtouchette/recon/pkg/recon`) so tools like Rivet can embed it in-process:

```go
r, _ := recon.New(".")
defer r.Close()

hot, _ := r.Hotspots(10)
rel, _ := r.Related("internal/orders/handler.go")
```

The CLI command tree is also importable (`github.com/djtouchette/recon/cmd/recon/cli`) for embedding the full command set into another binary.

## Building

```bash
make build       # build to bin/recon
make test        # run tests
make bench       # benchmarks
make lint        # golangci-lint
make clean       # remove bin/ and .recon/
```

Requires Go 1.25+ and a C compiler (tree-sitter uses CGo, so builds need `CGO_ENABLED=1` and a working `cc`/`gcc`).

## License

MIT
