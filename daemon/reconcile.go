package daemon

import (
	"regexp"
	"strings"
	"time"
)

// ReconcileState represents the health of a reconciled module.
type ReconcileState int

const (
	ReconHealthy  ReconcileState = iota
	ReconDegraded                // SLA breached but functional
	ReconDrifted                 // invariant violated
	ReconFailed                  // precondition failed
	ReconPaused                  // manually or automatically paused
)

func (s ReconcileState) String() string {
	switch s {
	case ReconHealthy:
		return "healthy"
	case ReconDegraded:
		return "degraded"
	case ReconDrifted:
		return "drifted"
	case ReconFailed:
		return "failed"
	case ReconPaused:
		return "paused"
	default:
		return "unknown"
	}
}

// ReconcileSpec is a parsed reconcile block for an effect.
type ReconcileSpec struct {
	Effect     string
	File       string
	Interval   time.Duration
	Invariants []InvariantSpec
	SLAs       []SLASpec
	OnDrift    []DriftAction
}

// InvariantSpec is a named invariant check.
type InvariantSpec struct {
	Name string
	Body string // the assertion code (for display/logging)
}

// SLASpec is a service level agreement.
type SLASpec struct {
	Name  string
	Rules []string // e.g., "p99 < 2s", "error_rate < 0.01"
}

// DriftAction is what to do when an invariant is violated.
type DriftAction struct {
	InvariantName string
	Actions       []string // e.g., "alert(\"oncall\")", "pause(Effect.Op)"
}

// ReconcileStatus tracks the live state of a reconciled module.
type ReconcileStatus struct {
	Spec       *ReconcileSpec
	State      ReconcileState
	LastCheck  time.Time
	LastError  string
	CheckCount int
	FailCount  int
}

// --- Parsing reconcile blocks from .tg source ---

var reconcileRe = regexp.MustCompile(`(?m)^reconcile\s+(\w+)\s*\{`)

// ParseReconcileSpecs extracts all reconcile blocks from a .tg file.
func ParseReconcileSpecs(src string, file string) []ReconcileSpec {
	var specs []ReconcileSpec

	matches := reconcileRe.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range matches {
		effectName := src[loc[2]:loc[3]]

		bracePos := loc[1] - 1
		endPos := findClosingBrace(src, bracePos)
		if endPos == -1 {
			continue
		}

		body := src[bracePos+1 : endPos-1]
		spec := parseReconcileBody(effectName, file, body)
		specs = append(specs, spec)
	}

	return specs
}

func parseReconcileBody(effect, file, body string) ReconcileSpec {
	spec := ReconcileSpec{
		Effect: effect,
		File:   file,
	}

	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])

		// interval: 30s
		if strings.HasPrefix(line, "interval:") {
			durStr := strings.TrimSpace(strings.TrimPrefix(line, "interval:"))
			if d, err := time.ParseDuration(durStr); err == nil {
				spec.Interval = d
			}
		}

		// invariant "name" { ... }
		if strings.HasPrefix(line, "invariant ") {
			name, bodyText, skip := parseNamedBlock(lines, i)
			spec.Invariants = append(spec.Invariants, InvariantSpec{
				Name: name,
				Body: bodyText,
			})
			i += skip
			continue
		}

		// sla "name" { ... }
		if strings.HasPrefix(line, "sla ") {
			name, bodyText, skip := parseNamedBlock(lines, i)
			var rules []string
			for _, r := range strings.Split(bodyText, "\n") {
				r = strings.TrimSpace(r)
				if r != "" {
					rules = append(rules, r)
				}
			}
			spec.SLAs = append(spec.SLAs, SLASpec{
				Name:  name,
				Rules: rules,
			})
			i += skip
			continue
		}

		// on_drift "name" { action: ... }
		if strings.HasPrefix(line, "on_drift ") {
			name, bodyText, skip := parseNamedBlock(lines, i)
			var actions []string
			for _, a := range strings.Split(bodyText, "\n") {
				a = strings.TrimSpace(a)
				if strings.HasPrefix(a, "action:") {
					actions = append(actions, strings.TrimSpace(strings.TrimPrefix(a, "action:")))
				}
			}
			spec.OnDrift = append(spec.OnDrift, DriftAction{
				InvariantName: name,
				Actions:       actions,
			})
			i += skip
			continue
		}

		i++
	}

	if spec.Interval == 0 {
		spec.Interval = 30 * time.Second // default
	}

	return spec
}

// parseNamedBlock parses: keyword "name" { body }
// Returns the name, body text, and number of lines consumed.
func parseNamedBlock(lines []string, startIdx int) (string, string, int) {
	line := strings.TrimSpace(lines[startIdx])

	// Extract name from quotes
	nameStart := strings.Index(line, `"`)
	nameEnd := strings.LastIndex(line, `"`)
	name := ""
	if nameStart != -1 && nameEnd > nameStart {
		name = line[nameStart+1 : nameEnd]
	}

	// Find opening brace
	if !strings.Contains(line, "{") {
		return name, "", 1
	}

	// Collect body until closing brace
	depth := 0
	var bodyLines []string
	for i := startIdx; i < len(lines); i++ {
		l := lines[i]
		for _, ch := range l {
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
			}
		}
		if i > startIdx {
			bodyLines = append(bodyLines, l)
		}
		if depth == 0 {
			// Remove the closing brace line
			if len(bodyLines) > 0 {
				last := bodyLines[len(bodyLines)-1]
				last = strings.TrimSuffix(strings.TrimSpace(last), "}")
				bodyLines[len(bodyLines)-1] = last
			}
			return name, strings.Join(bodyLines, "\n"), i - startIdx + 1
		}
	}

	return name, strings.Join(bodyLines, "\n"), len(lines) - startIdx
}

// StripReconcileBlocks removes reconcile blocks from source before transpilation
// (they're metadata, not runtime code).
func StripReconcileBlocks(src string) string {
	for {
		loc := reconcileRe.FindStringIndex(src)
		if loc == nil {
			break
		}
		bracePos := strings.Index(src[loc[0]:], "{")
		if bracePos == -1 {
			break
		}
		bracePos += loc[0]
		endPos := findClosingBrace(src, bracePos)
		if endPos == -1 {
			break
		}
		// Remove block and trailing newlines
		end := endPos
		for end < len(src) && (src[end] == '\n' || src[end] == '\r') {
			end++
		}
		src = src[:loc[0]] + src[end:]
	}
	return src
}
