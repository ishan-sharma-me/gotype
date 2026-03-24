package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventKind identifies the type of daemon event.
type EventKind int

const (
	EventFileChanged EventKind = iota
	EventTestStarted
	EventTestCompleted
	EventGraphChanged
)

// Event is emitted by the daemon for the feed.
type Event struct {
	Kind      EventKind   `json:"kind"`
	Timestamp time.Time   `json:"timestamp"`
	Files     []string    `json:"files,omitempty"`
	Module    string      `json:"module,omitempty"`
	Results   []TestResult `json:"results,omitempty"`
}

// OnEvent is called when the daemon produces an event.
type OnEvent func(Event)

// Daemon is the Ananke daemon — watches files, tracks the module graph,
// and runs tests with cascade invalidation.
type Daemon struct {
	root    string
	graph   *Graph
	runner  *Runner
	watcher *Watcher
	onEvent OnEvent
	done    chan struct{}
}

// New creates a new daemon for the given project root.
func New(root string) (*Daemon, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	d := &Daemon{
		root:   abs,
		graph:  NewGraph(abs),
		runner: NewRunner(abs),
		done:   make(chan struct{}),
	}

	return d, nil
}

// OnEventFunc sets the event callback.
func (d *Daemon) OnEventFunc(fn OnEvent) {
	d.onEvent = fn
}

// emit sends an event to the callback if set.
func (d *Daemon) emit(e Event) {
	e.Timestamp = time.Now()
	if d.onEvent != nil {
		d.onEvent(e)
	}
}

// RunOnce scans the project and builds the graph without watching.
// Used for one-shot commands like `status` and `graph`.
func (d *Daemon) RunOnce() error {
	return d.graph.Scan()
}

// Run starts the daemon. Blocks until Stop is called.
func (d *Daemon) Run() error {
	log.Printf("Ananke starting in %s", d.root)

	// Initial scan
	if err := d.graph.Scan(); err != nil {
		return fmt.Errorf("scanning project: %w", err)
	}
	log.Printf("Found %d effects, %d modules", len(d.graph.Effects), len(d.graph.Modules))

	// Run all tests on startup
	d.runAllTests()

	// Start file watcher
	watcher, err := NewWatcher(d.root, d.onFilesChanged)
	if err != nil {
		return fmt.Errorf("starting watcher: %w", err)
	}
	d.watcher = watcher

	log.Println("Watching for changes...")
	watcher.Run(d.done)
	return nil
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	close(d.done)
}

// Status returns a human-readable status summary.
func (d *Daemon) Status() string {
	d.graph.mu.RLock()
	defer d.graph.mu.RUnlock()

	var healthy, failing, stale, unknown int
	for _, m := range d.graph.Modules {
		switch m.State {
		case StateHealthy:
			healthy++
		case StateFailing:
			failing++
		case StateStale, StateTainted:
			stale++
		default:
			unknown++
		}
	}

	return fmt.Sprintf("Effects: %d | Modules: %d (healthy: %d, failing: %d, stale: %d, unknown: %d)",
		len(d.graph.Effects), len(d.graph.Modules), healthy, failing, stale, unknown)
}

// Graph returns the module graph description.
func (d *Daemon) Graph() string {
	return d.graph.Print()
}

// GraphData returns the underlying graph for direct access (used by feed server).
func (d *Daemon) GraphData() *Graph {
	return d.graph
}

// TriggerFullRun re-runs all tests.
func (d *Daemon) TriggerFullRun() {
	d.runAllTests()
}

// onFilesChanged is called by the watcher when .tg files change.
func (d *Daemon) onFilesChanged(files []string) {
	d.emit(Event{Kind: EventFileChanged, Files: files})

	// Re-scan changed files
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(d.root, f)
		d.graph.ScanFile(rel, string(src))
	}

	d.emit(Event{Kind: EventGraphChanged})

	// Find affected modules
	affected := d.graph.Invalidate(files)
	if len(affected) == 0 {
		log.Println("No modules affected by change")
		return
	}

	names := make([]string, len(affected))
	for i, m := range affected {
		names[i] = m.Name
	}
	log.Printf("Affected modules: %s", strings.Join(names, ", "))

	// Mark affected as stale
	d.graph.mu.Lock()
	for _, m := range affected {
		m.State = StateStale
	}
	d.graph.mu.Unlock()

	// Collect files to test
	fileSet := make(map[string]bool)
	for _, m := range affected {
		fileSet[m.File] = true
	}
	var testFiles []string
	for f := range fileSet {
		testFiles = append(testFiles, f)
	}

	// Run tests
	d.runTestsForFiles(testFiles, affected)
}

// runAllTests runs tests for all known modules.
func (d *Daemon) runAllTests() {
	d.graph.mu.RLock()
	fileSet := make(map[string]bool)
	var modules []*Module
	for _, m := range d.graph.Modules {
		fileSet[m.File] = true
		modules = append(modules, m)
	}
	d.graph.mu.RUnlock()

	if len(fileSet) == 0 {
		return
	}

	var files []string
	for f := range fileSet {
		files = append(files, f)
	}

	d.runTestsForFiles(files, modules)
}

// runTestsForFiles transpiles and runs tests, then updates module states.
func (d *Daemon) runTestsForFiles(files []string, modules []*Module) {
	// Mark as running
	d.graph.mu.Lock()
	for _, m := range modules {
		m.State = StateRunning
	}
	d.graph.mu.Unlock()

	for _, m := range modules {
		d.emit(Event{Kind: EventTestStarted, Module: m.Name})
	}

	results, err := d.runner.RunTests(files)
	if err != nil {
		log.Printf("Test run error: %v", err)
		return
	}

	// Update module states based on results
	d.graph.mu.Lock()
	// Reset all affected to healthy first
	for _, m := range modules {
		m.State = StateHealthy
		m.LastRun = time.Now()
		m.LastErr = ""
	}

	// Mark failures
	for _, r := range results {
		if r.Status == "fail" || r.Status == "error" {
			// Try to match result to a module
			for _, m := range modules {
				if strings.Contains(r.Test, m.Name) || m.File == r.Module {
					m.State = StateFailing
					m.LastErr = r.Output
				}
			}
		}
	}

	// Cascade: if a module is failing, taint its dependents
	for _, m := range d.graph.Modules {
		if m.State != StateFailing {
			continue
		}
		for _, dep := range d.graph.Modules {
			if dep.Name == m.Name {
				continue
			}
			for _, e := range dep.Performs {
				for _, me := range m.Performs {
					if e == me && dep.State == StateHealthy {
						dep.State = StateTainted
					}
				}
			}
		}
	}
	d.graph.mu.Unlock()

	d.emit(Event{Kind: EventTestCompleted, Results: results})

	// Log results
	passed, failed := 0, 0
	for _, r := range results {
		if r.Status == "pass" {
			passed++
		} else {
			failed++
			log.Printf("FAIL: %s — %s", r.Test, strings.TrimSpace(r.Output))
		}
	}
	log.Printf("Tests: %d passed, %d failed", passed, failed)
}
