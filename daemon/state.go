package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ModuleState represents the health of a module.
type ModuleState int

const (
	StateUnknown  ModuleState = iota
	StateHealthy              // all tests pass
	StateFailing              // tests failed
	StateStale                // needs re-test (dependency changed)
	StateTainted              // dependency is failing
	StateRunning              // tests currently executing
)

func (s ModuleState) String() string {
	switch s {
	case StateHealthy:
		return "healthy"
	case StateFailing:
		return "failing"
	case StateStale:
		return "stale"
	case StateTainted:
		return "tainted"
	case StateRunning:
		return "running"
	default:
		return "unknown"
	}
}

// Module represents a function that performs effects — the unit of testing.
type Module struct {
	Name     string      // function name
	File     string      // source .tg file (relative to root)
	Performs []string    // effect names this function performs
	State    ModuleState
	LastRun  time.Time
	LastErr  string // last test error message, if any
}

// Effect represents a declared effect.
type Effect struct {
	Name string
	File string
}

// Graph is the module dependency graph derived from effect declarations and perform calls.
type Graph struct {
	mu       sync.RWMutex
	root     string
	Effects  map[string]*Effect  // effect name → declaration
	Modules  map[string]*Module  // function name → module
	performs map[string][]string // module name → []effect names
}

// NewGraph creates an empty graph.
func NewGraph(root string) *Graph {
	return &Graph{
		root:     root,
		Effects:  make(map[string]*Effect),
		Modules:  make(map[string]*Module),
		performs: make(map[string][]string),
	}
}

var (
	effectRe  = regexp.MustCompile(`(?m)^effect\s+([A-Z]\w*)`)
	funcRe    = regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(`)
	performRe = regexp.MustCompile(`perform\s+([A-Z]\w*)`)
)

// Scan reads all .tg files under root and builds the module graph.
func (g *Graph) Scan() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.Effects = make(map[string]*Effect)
	g.Modules = make(map[string]*Module)
	g.performs = make(map[string][]string)

	return filepath.Walk(g.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".tg") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(g.root, path)
		g.scanFile(rel, string(src))
		return nil
	})
}

// ScanFile updates the graph with the contents of a single file.
func (g *Graph) ScanFile(relPath string, src string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Remove old entries from this file
	for name, e := range g.Effects {
		if e.File == relPath {
			delete(g.Effects, name)
		}
	}
	for name, m := range g.Modules {
		if m.File == relPath {
			delete(g.Modules, name)
			delete(g.performs, name)
		}
	}

	g.scanFile(relPath, src)
}

func (g *Graph) scanFile(relPath string, src string) {
	// Collect effect declarations
	for _, m := range effectRe.FindAllStringSubmatch(src, -1) {
		g.Effects[m[1]] = &Effect{Name: m[1], File: relPath}
	}

	// Find functions and what they perform
	funcMatches := funcRe.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range funcMatches {
		name := src[loc[2]:loc[3]]
		if name == "main" {
			continue
		}

		bodyStart := findBodyBrace(src, loc[1])
		if bodyStart == -1 {
			continue
		}
		bodyEnd := findClosingBrace(src, bodyStart)
		if bodyEnd == -1 {
			continue
		}

		body := src[bodyStart:bodyEnd]
		var performs []string
		for _, pm := range performRe.FindAllStringSubmatch(body, -1) {
			performs = append(performs, pm[1])
		}

		if len(performs) > 0 {
			g.Modules[name] = &Module{
				Name:     name,
				File:     relPath,
				Performs: performs,
				State:    StateUnknown,
			}
			g.performs[name] = performs
		}
	}

	// Propagate: functions that call effectful functions are also effectful
	changed := true
	for changed {
		changed = false
		for _, floc := range funcMatches {
			name := src[floc[2]:floc[3]]
			if name == "main" || g.Modules[name] != nil {
				continue
			}

			bodyStart := findBodyBrace(src, floc[1])
			if bodyStart == -1 {
				continue
			}
			bodyEnd := findClosingBrace(src, bodyStart)
			if bodyEnd == -1 {
				continue
			}
			body := src[bodyStart:bodyEnd]

			for callee := range g.Modules {
				if strings.Contains(body, callee+"(") {
					// Inherit effects from callee
					g.Modules[name] = &Module{
						Name:     name,
						File:     relPath,
						Performs: g.performs[callee],
						State:    StateUnknown,
					}
					g.performs[name] = g.performs[callee]
					changed = true
					break
				}
			}
		}
	}
}

// Invalidate returns the set of modules that need re-testing when the given files change.
func (g *Graph) Invalidate(changedFiles []string) []*Module {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Find modules in changed files
	affected := make(map[string]*Module)
	for _, file := range changedFiles {
		rel, _ := filepath.Rel(g.root, file)
		for _, m := range g.Modules {
			if m.File == rel {
				affected[m.Name] = m
			}
		}
	}

	// Find modules whose effects overlap with changed modules' effects
	changedEffects := make(map[string]bool)
	for _, m := range affected {
		for _, e := range m.Performs {
			changedEffects[e] = true
		}
	}

	// Any module that performs a changed effect is also affected
	for _, m := range g.Modules {
		if affected[m.Name] != nil {
			continue
		}
		for _, e := range m.Performs {
			if changedEffects[e] {
				affected[m.Name] = m
				break
			}
		}
	}

	result := make([]*Module, 0, len(affected))
	for _, m := range affected {
		result = append(result, m)
	}
	return result
}

// Mu returns the graph's read-write mutex for external synchronization.
func (g *Graph) Mu() *sync.RWMutex {
	return &g.mu
}

// Print outputs the graph in a human-readable format.
func (g *Graph) Print() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var b strings.Builder

	if len(g.Effects) > 0 {
		b.WriteString("Effects:\n")
		for _, e := range g.Effects {
			b.WriteString(fmt.Sprintf("  %s (%s)\n", e.Name, e.File))
		}
		b.WriteString("\n")
	}

	if len(g.Modules) > 0 {
		b.WriteString("Modules:\n")
		for _, m := range g.Modules {
			b.WriteString(fmt.Sprintf("  %s [%s] (%s)\n", m.Name, m.State, m.File))
			for _, p := range m.Performs {
				b.WriteString(fmt.Sprintf("    └── performs %s\n", p))
			}
		}
	}

	return b.String()
}

func findBodyBrace(src string, pos int) int {
	depth := 1
	for pos < len(src) {
		switch src[pos] {
		case '(':
			depth++
		case ')':
			depth--
		case '{':
			if depth == 0 {
				return pos
			}
		}
		pos++
	}
	return -1
}

func findClosingBrace(src string, pos int) int {
	if pos >= len(src) || src[pos] != '{' {
		return -1
	}
	depth := 0
	for pos < len(src) {
		switch src[pos] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return pos + 1
			}
		case '"':
			pos++
			for pos < len(src) {
				if src[pos] == '\\' {
					pos++
				} else if src[pos] == '"' {
					break
				}
				pos++
			}
		case '`':
			pos++
			for pos < len(src) && src[pos] != '`' {
				pos++
			}
		}
		pos++
	}
	return -1
}
