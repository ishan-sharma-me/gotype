# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**gotype** is a `.tg` → `.go` transpiler where `.tg` is a **strict superset of Go**. It adds algebraic effects, sum types, pattern matching, pipeline operators, and structured concurrency. `perform` is the module boundary — the same effect graph drives test cascading in dev and reconciliation in prod via **Ananke** (the daemon).

The end-goal: an infrastructure framework for AI/LLM agents where every side effect (LLM, tools, memory, budget) is a typed, testable, reconcilable effect.

Three executables:
- `gotype` — transpiler CLI (run, build, transpile, test, graph)
- `gotype-lsp` — language server (diagnostics, completions, hover, go-to-definition via gopls proxy)
- `gotyped` — Ananke daemon (watches files, cascading tests in dev, reconciliation in prod, SSE feed for AI)

## Commands

```bash
# Install (builds gotype + gotype-lsp, adds ~/.gotype/bin to PATH)
make install

# Install with VS Code extension
make install-lsp

# Build/test
go build ./...
go test ./...                              # all tests
go test ./conformance/ -timeout 120s       # Go spec conformance (2592 files)
make test-conformance                      # same, with summary output
go vet ./...

# Use the transpiler
gotype run file.tg                 # transpile + run
gotype transpile file.tg           # emit .go
gotype transpile file.tg -o outdir
gotype build file.tg               # transpile + go build
```

## Architecture

### Core constraint: .tg ⊇ Go
Every valid `.go` file is a valid `.tg` file. All Go modules/imports work directly. The transpiler uses Go's own `go/parser`, `go/ast`, `go/printer` — we only handle the syntax delta.

### Transpiler pipeline
```
.tg → [Preprocessor] → [go/parser] → [Effect Extractor] → [Analyzer] → [Transformer] → [go/printer] → .go
```

The preprocessor (`preprocess/effects.go`) rewrites effect syntax into self-contained Go before `go/parser` sees it. **No runtime library** — like TypeScript→JavaScript, the output is plain Go using only goroutines and channels. A small `__effReq` struct is generated inline.

### Package layout
- `cmd/gotype/` — transpiler CLI
- `cmd/gotype-lsp/` — LSP server binary
- `cmd/gotyped/` — Ananke daemon CLI (planned)
- `preprocess/` — rewrite .tg syntax into parseable Go (`scanner.go` finds constructs, `effects.go` transforms them)
- `lsp/` — language server: JSON-RPC transport, shadow project, gopls proxy, completions
- `runtime/` — reference implementation of effect mechanism (used for testing semantics, NOT imported by generated code)
- `effectast/` — effect-specific AST nodes
- `analyzer/` — effect type checking and propagation
- `transform/` — rewrite AST: effects → Go runtime calls
- `module/` — module detection from `performs` declarations, dependency graph
- `daemon/` — file watcher, scheduler, test runner, validator, reconciliation
- `feed/` — HTTP/SSE server for AI feed
- `conformance/` — Go spec conformance tests (validates .tg ⊇ Go using 2592 files from Go's own test suite)
- `editor/vscode/` — VS Code extension (.tg language support + LSP client)
- `testdata/` — example .tg files
- `testdata/go-spec/` — Go compiler source (git submodule) used for conformance testing

### .tg syntax (additions to Go)
```go
// Effects — simple declarations (IMPLEMENTED)
effect AskName(string)
effect Log(msg string)
effect Charge(customerID string, amount int) (Receipt, error)

// Perform — raises effect, blocks until handled (IMPLEMENTED)
name := perform AskName.(string)
perform Log("hello")

// Handle/with — catch effects with case clauses (IMPLEMENTED)
handle {
    makeFriends(arya, gendry)
} with {
case AskName:
    resume("Arya Stark")
case Log(msg string):
    fmt.Println(msg)
    resume()
}

// Tests + contracts (Phase 3)
test "name works" {
    handle { assert getName() == "Alice" }
    with { case AskName: resume("Alice") }
}
contract AskName { test "returns non-empty" { ... } }

// Sum types + pattern matching (Phase 6)
type Result = Success{value string} | Failure{err error}
match result { case Success{v}: fmt.Println(v); case Failure{e}: log.Fatal(e) }

// Pipeline operator (Phase 6)
result := data |> transform |> validate |> save

// Do-notation (Phase 6)
result := effect.do {
    user <- getUser(id)
    profile <- getProfile(user.ID)
    return profile
}

// Structured concurrency (Phase 9)
parallel { branch "a" { ... }; branch "b" { ... } }
race { branch "cache" { ... }; branch "db" { ... } }
timeout 5s { ... } on_timeout { ... }

// Reconciliation (Phase 8)
reconcile PaymentGateway {
    interval: 30s
    invariant "consistency" { assert drift < threshold }
    sla "latency" { p99 < 2s }
    on_drift { action: alert("oncall"); action: pause(Effect.Op) }
}
```

### Codegen design (no runtime — self-contained Go output)
- `perform Name` → inline anonymous func: creates response channel, sends `__effReq` on `__eff`, blocks on response
- `handle { body } with { cases }` → `func(){}()` wrapper: creates `__eff` channel, runs body in goroutine, select loop dispatches to switch cases
- `resume(val)` → `__req.rch <- val` (direct channel send)
- Generated type: `type __effReq struct{ name string; args []any; rch chan any }` (injected once per file)
- Effects bubble up through call stack via `__eff chan<- __effReq` parameter passing

### LSP architecture
`gotype-lsp` proxies to `gopls` via a shadow project. Preprocesses `.tg` → `.go` into `~/.cache/gotype-lsp/<hash>/`, forwards LSP requests with URI translation. Effect keyword completions merged on top.

### Ananke (daemon) design
Same effect dependency graph serves two modes:
- **Dev:** file watching → incremental parse → module graph diff → cascade test scheduling → SSE feed to AI
- **Prod:** scheduled reconciliation → invariant/SLA checking → cascade state propagation → alerts/fallbacks
