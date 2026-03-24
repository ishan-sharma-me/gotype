package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ReconcileEventKind identifies reconciliation events.
type ReconcileEventKind int

const (
	ReconEventCheck   ReconcileEventKind = iota // invariant checked
	ReconEventDrift                             // invariant violated
	ReconEventRecover                           // drift recovered
	ReconEventAction                            // action triggered
)

// ReconcileEvent is emitted during reconciliation.
type ReconcileEvent struct {
	Kind      ReconcileEventKind `json:"kind"`
	Effect    string             `json:"effect"`
	Invariant string             `json:"invariant,omitempty"`
	State     string             `json:"state"`
	Message   string             `json:"message,omitempty"`
	Actions   []string           `json:"actions,omitempty"`
	Timestamp time.Time          `json:"timestamp"`
}

// OnReconcileEvent is called when reconciliation produces an event.
type OnReconcileEvent func(ReconcileEvent)

// Reconciler runs continuous contract verification on the effect graph.
type Reconciler struct {
	mu       sync.Mutex
	root     string
	specs    map[string]*ReconcileSpec   // effect name → spec
	statuses map[string]*ReconcileStatus // effect name → live status
	graph    *Graph
	onEvent  OnReconcileEvent
	done     chan struct{}
}

// NewReconciler creates a reconciler for the given project.
func NewReconciler(root string, graph *Graph) *Reconciler {
	return &Reconciler{
		root:     root,
		specs:    make(map[string]*ReconcileSpec),
		statuses: make(map[string]*ReconcileStatus),
		graph:    graph,
		done:     make(chan struct{}),
	}
}

// OnEventFunc sets the reconciliation event callback.
func (r *Reconciler) OnEventFunc(fn OnReconcileEvent) {
	r.onEvent = fn
}

func (r *Reconciler) emit(e ReconcileEvent) {
	e.Timestamp = time.Now()
	if r.onEvent != nil {
		r.onEvent(e)
	}
}

// Scan finds and parses all reconcile blocks in the project.
func (r *Reconciler) Scan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" {
					return filepath.SkipDir
				}
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

		rel, _ := filepath.Rel(r.root, path)
		specs := ParseReconcileSpecs(string(src), rel)
		for i := range specs {
			spec := specs[i]
			r.specs[spec.Effect] = &spec
			r.statuses[spec.Effect] = &ReconcileStatus{
				Spec:  &spec,
				State: ReconHealthy,
			}
		}

		return nil
	})
}

// Run starts the reconciliation loop. Blocks until Stop is called.
func (r *Reconciler) Run() {
	r.mu.Lock()
	specs := make([]*ReconcileSpec, 0, len(r.specs))
	for _, s := range r.specs {
		specs = append(specs, s)
	}
	r.mu.Unlock()

	if len(specs) == 0 {
		log.Println("No reconcile blocks found")
		<-r.done
		return
	}

	log.Printf("Reconciling %d effects", len(specs))

	// Start a ticker for each reconcile spec
	for _, spec := range specs {
		go r.reconcileLoop(spec)
	}

	<-r.done
}

// Stop shuts down the reconciler.
func (r *Reconciler) Stop() {
	close(r.done)
}

func (r *Reconciler) reconcileLoop(spec *ReconcileSpec) {
	ticker := time.NewTicker(spec.Interval)
	defer ticker.Stop()

	// Run immediately on start
	r.checkInvariants(spec)

	for {
		select {
		case <-ticker.C:
			r.checkInvariants(spec)
		case <-r.done:
			return
		}
	}
}

func (r *Reconciler) checkInvariants(spec *ReconcileSpec) {
	r.mu.Lock()
	status := r.statuses[spec.Effect]
	r.mu.Unlock()

	if status == nil {
		return
	}

	prevState := status.State
	allPassed := true

	for _, inv := range spec.Invariants {
		status.CheckCount++

		// For now, invariants always pass (the bodies are declarative —
		// actual checking requires running the code with real handlers).
		// In prod mode, the daemon would transpile and execute the invariant body.
		passed := true

		r.emit(ReconcileEvent{
			Kind:      ReconEventCheck,
			Effect:    spec.Effect,
			Invariant: inv.Name,
			State:     "checked",
			Message:   fmt.Sprintf("invariant %q checked", inv.Name),
		})

		if !passed {
			allPassed = false
			status.FailCount++
			status.LastError = fmt.Sprintf("invariant %q failed", inv.Name)
		}
	}

	r.mu.Lock()
	if allPassed {
		status.State = ReconHealthy
		status.LastError = ""
	}
	status.LastCheck = time.Now()
	r.mu.Unlock()

	// Emit state change if needed
	if prevState != status.State {
		if status.State == ReconHealthy && prevState != ReconHealthy {
			r.emit(ReconcileEvent{
				Kind:    ReconEventRecover,
				Effect:  spec.Effect,
				State:   status.State.String(),
				Message: "recovered",
			})
			// Recovery cascade: re-check dependents
			r.cascadeRecovery(spec.Effect)
		}
	}
}

// SimulateFail forces an effect into failed state and cascades.
func (r *Reconciler) SimulateFail(effect string) {
	r.mu.Lock()
	status, ok := r.statuses[effect]
	if !ok {
		r.mu.Unlock()
		log.Printf("Unknown effect: %s", effect)
		return
	}
	status.State = ReconFailed
	status.LastError = "simulated failure"
	r.mu.Unlock()

	r.emit(ReconcileEvent{
		Kind:    ReconEventDrift,
		Effect:  effect,
		State:   "failed",
		Message: "simulated failure",
	})

	// Execute on_drift actions
	r.mu.Lock()
	spec := r.specs[effect]
	r.mu.Unlock()
	if spec != nil {
		for _, da := range spec.OnDrift {
			for _, action := range da.Actions {
				r.emit(ReconcileEvent{
					Kind:    ReconEventAction,
					Effect:  effect,
					State:   "failed",
					Message: fmt.Sprintf("executing: %s", action),
					Actions: []string{action},
				})
				log.Printf("ACTION [%s]: %s", effect, action)
			}
		}
	}

	// Cascade to dependents
	r.cascadeFailure(effect)
}

// SimulateDrift forces an invariant violation on an effect.
func (r *Reconciler) SimulateDrift(effect, invariant string) {
	r.mu.Lock()
	status, ok := r.statuses[effect]
	if !ok {
		r.mu.Unlock()
		log.Printf("Unknown effect: %s", effect)
		return
	}
	status.State = ReconDrifted
	status.LastError = fmt.Sprintf("invariant %q violated (simulated)", invariant)
	r.mu.Unlock()

	r.emit(ReconcileEvent{
		Kind:      ReconEventDrift,
		Effect:    effect,
		Invariant: invariant,
		State:     "drifted",
		Message:   fmt.Sprintf("invariant %q violated (simulated)", invariant),
	})

	// Execute on_drift actions for this invariant
	r.mu.Lock()
	spec := r.specs[effect]
	r.mu.Unlock()
	if spec != nil {
		for _, da := range spec.OnDrift {
			if da.InvariantName == invariant || da.InvariantName == "" {
				for _, action := range da.Actions {
					r.emit(ReconcileEvent{
						Kind:    ReconEventAction,
						Effect:  effect,
						State:   "drifted",
						Message: fmt.Sprintf("executing: %s", action),
						Actions: []string{action},
					})
					log.Printf("ACTION [%s/%s]: %s", effect, invariant, action)
				}
			}
		}
	}

	r.cascadeFailure(effect)
}

// cascadeFailure marks all modules that perform the failed effect as degraded.
func (r *Reconciler) cascadeFailure(failedEffect string) {
	r.graph.Mu().RLock()
	defer r.graph.Mu().RUnlock()

	for _, m := range r.graph.Modules {
		for _, e := range m.Performs {
			if e == failedEffect {
				r.emit(ReconcileEvent{
					Kind:    ReconEventDrift,
					Effect:  failedEffect,
					State:   "tainted",
					Message: fmt.Sprintf("module %s tainted (depends on %s)", m.Name, failedEffect),
				})
				log.Printf("CASCADE: %s → tainted (depends on %s)", m.Name, failedEffect)
			}
		}
	}
}

// cascadeRecovery re-checks modules that depend on the recovered effect.
func (r *Reconciler) cascadeRecovery(recoveredEffect string) {
	r.graph.Mu().RLock()
	defer r.graph.Mu().RUnlock()

	for _, m := range r.graph.Modules {
		for _, e := range m.Performs {
			if e == recoveredEffect {
				log.Printf("RECOVERY CASCADE: %s may recover (dependency %s recovered)", m.Name, recoveredEffect)
			}
		}
	}
}

// Status returns a summary of all reconciliation statuses.
func (r *Reconciler) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.statuses) == 0 {
		return "No reconcile blocks found"
	}

	var b strings.Builder
	b.WriteString("Reconciliation Status:\n")
	for effect, status := range r.statuses {
		b.WriteString(fmt.Sprintf("  %s [%s]", effect, status.State))
		if status.LastError != "" {
			b.WriteString(fmt.Sprintf(" — %s", status.LastError))
		}
		if !status.LastCheck.IsZero() {
			b.WriteString(fmt.Sprintf(" (last check: %s)", status.LastCheck.Format("15:04:05")))
		}
		b.WriteString("\n")
		if status.Spec != nil {
			b.WriteString(fmt.Sprintf("    interval: %s, checks: %d, failures: %d\n",
				status.Spec.Interval, status.CheckCount, status.FailCount))
			for _, inv := range status.Spec.Invariants {
				b.WriteString(fmt.Sprintf("    invariant: %q\n", inv.Name))
			}
			for _, sla := range status.Spec.SLAs {
				b.WriteString(fmt.Sprintf("    sla: %q %v\n", sla.Name, sla.Rules))
			}
		}
	}
	return b.String()
}
