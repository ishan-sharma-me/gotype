package preprocess

import (
	"fmt"
	"regexp"
	"strings"
)

// TransformConcurrency rewrites structured concurrency syntax into Go.
// Called AFTER TransformEffects (branches may contain perform).
//
// Transforms:
//
//	parallel {
//	    branch "name" { body }
//	    branch "name" { body }
//	}
//	→ sync.WaitGroup + goroutines (all must complete)
//
//	race {
//	    branch "name" { body }
//	    branch "name" { body }
//	}
//	→ goroutines + channel, first result wins, context cancel for others
//
//	timeout 5s {
//	    body
//	} on_timeout {
//	    fallback
//	}
//	→ context.WithTimeout + select
func TransformConcurrency(src string) string {
	if !containsConcurrencySyntax(src) {
		return src
	}

	src = ensureSyncImport(src)

	s := newScanner(src)
	for s.pos < len(s.src) {
		ch := s.src[s.pos]

		if ch == '"' || ch == '`' || ch == '\'' {
			s.skipString()
			continue
		}
		if ch == '/' && s.pos+1 < len(s.src) {
			if s.src[s.pos+1] == '/' {
				s.skipLineComment()
				continue
			}
			if s.src[s.pos+1] == '*' {
				s.skipBlockComment()
				continue
			}
		}

		if s.atWord("parallel") {
			if s.transformParallel() {
				continue
			}
		}

		if s.atWord("race") {
			if s.transformRace() {
				continue
			}
		}

		if s.atWord("timeout") {
			if s.transformTimeout() {
				continue
			}
		}

		s.advance(1)
	}

	return s.output.String()
}

func containsConcurrencySyntax(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "parallel {") ||
			strings.HasPrefix(trimmed, "parallel\t{") ||
			strings.HasPrefix(trimmed, "race {") ||
			strings.HasPrefix(trimmed, "race\t{") ||
			strings.HasPrefix(trimmed, "timeout ") {
			return true
		}
	}
	return false
}

func ensureSyncImport(src string) string {
	// Only add imports for constructs that are actually present as statements
	hasParallel := false
	hasRace := false
	hasTimeout := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "parallel {") || strings.HasPrefix(t, "parallel\t{") {
			hasParallel = true
		}
		if strings.HasPrefix(t, "race {") || strings.HasPrefix(t, "race\t{") {
			hasRace = true
		}
		if strings.HasPrefix(t, "timeout ") {
			hasTimeout = true
		}
	}

	needsSync := hasParallel || hasRace
	needsContext := hasTimeout || hasRace
	needsTime := hasTimeout

	var imports []string
	if needsSync && !strings.Contains(src, `"sync"`) {
		imports = append(imports, `"sync"`)
	}
	if needsContext && !strings.Contains(src, `"context"`) {
		imports = append(imports, `"context"`)
	}
	if needsTime && !strings.Contains(src, `"time"`) {
		imports = append(imports, `"time"`)
	}

	if len(imports) == 0 {
		return src
	}

	if idx := strings.Index(src, "import ("); idx != -1 {
		insertAt := idx + len("import (")
		return src[:insertAt] + "\n\t" + strings.Join(imports, "\n\t") + src[insertAt:]
	}

	if idx := strings.Index(src, "import "); idx != -1 {
		lineEnd := strings.Index(src[idx:], "\n")
		if lineEnd != -1 {
			existing := strings.TrimSpace(src[idx+len("import ") : idx+lineEnd])
			all := append([]string{existing}, imports...)
			newImport := "import (\n\t" + strings.Join(all, "\n\t") + "\n)"
			return src[:idx] + newImport + src[idx+lineEnd:]
		}
	}

	return src
}

// --- parallel ---
// parallel { branch "a" { body1 } branch "b" { body2 } }
// →
// func() {
//     var __wg sync.WaitGroup
//     __wg.Add(2)
//     go func() { defer __wg.Done(); body1 }()
//     go func() { defer __wg.Done(); body2 }()
//     __wg.Wait()
// }()

func (s *scanner) transformParallel() bool {
	saved := s.pos
	s.skip(len("parallel"))
	s.skipWS()

	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		s.pos = saved
		return false
	}

	body, afterBody := extractBraceContent(s.src, s.pos)
	s.pos = afterBody

	branches := parseBranches(body)
	if len(branches) == 0 {
		s.pos = saved
		return false
	}

	var out strings.Builder
	out.WriteString("func() {\n")
	out.WriteString(fmt.Sprintf("\tvar __wg sync.WaitGroup\n"))
	out.WriteString(fmt.Sprintf("\t__wg.Add(%d)\n", len(branches)))

	for _, br := range branches {
		out.WriteString(fmt.Sprintf("\tgo func() { // %s\n", br.name))
		out.WriteString("\t\tdefer __wg.Done()\n")
		out.WriteString(fmt.Sprintf("\t\t%s\n", strings.TrimSpace(br.body)))
		out.WriteString("\t}()\n")
	}

	out.WriteString("\t__wg.Wait()\n")
	out.WriteString("}()")

	s.emit(out.String())
	return true
}

// --- race ---
// race { branch "a" { body1 } branch "b" { body2 } }
// →
// func() any {
//     ctx, cancel := context.WithCancel(context.Background())
//     defer cancel()
//     __ch := make(chan any, N)
//     go func() { ... __ch <- result }()
//     go func() { ... __ch <- result }()
//     return <-__ch
// }()

func (s *scanner) transformRace() bool {
	saved := s.pos
	s.skip(len("race"))
	s.skipWS()

	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		s.pos = saved
		return false
	}

	body, afterBody := extractBraceContent(s.src, s.pos)
	s.pos = afterBody

	branches := parseBranches(body)
	if len(branches) == 0 {
		s.pos = saved
		return false
	}

	var out strings.Builder
	out.WriteString("func() any {\n")
	out.WriteString("\t__ctx, __cancel := context.WithCancel(context.Background())\n")
	out.WriteString("\tdefer __cancel()\n")
	out.WriteString(fmt.Sprintf("\t__ch := make(chan any, %d)\n", len(branches)))

	for _, br := range branches {
		// Replace "return expr" with "__ch <- expr; return" in branch body
		brBody := transformBranchReturn(br.body)
		out.WriteString(fmt.Sprintf("\tgo func() { // %s\n", br.name))
		out.WriteString(fmt.Sprintf("\t\t_ = __ctx\n"))
		out.WriteString(fmt.Sprintf("\t\t%s\n", strings.TrimSpace(brBody)))
		out.WriteString("\t}()\n")
	}

	out.WriteString("\treturn <-__ch\n")
	out.WriteString("}()")

	s.emit(out.String())
	return true
}

// transformBranchReturn replaces `return expr` with `__ch <- expr; return`
func transformBranchReturn(body string) string {
	returnRe := regexp.MustCompile(`(?m)return\s+(.+)$`)
	return returnRe.ReplaceAllString(body, "__ch <- $1; return")
}

// --- timeout ---
// timeout 5s { body } on_timeout { fallback }
// →
// func() {
//     __ctx, __cancel := context.WithTimeout(context.Background(), 5*time.Second)
//     defer __cancel()
//     __done := make(chan struct{})
//     go func() { defer close(__done); body }()
//     select {
//     case <-__done:
//     case <-__ctx.Done():
//         fallback
//     }
// }()

var timeoutDurRe = regexp.MustCompile(`^(\d+)(ms|s|m|h)$`)

func (s *scanner) transformTimeout() bool {
	saved := s.pos
	s.skip(len("timeout"))
	s.skipWS()

	// Read duration: 5s, 100ms, 2m, etc.
	durStart := s.pos
	for s.pos < len(s.src) && s.src[s.pos] != ' ' && s.src[s.pos] != '\t' && s.src[s.pos] != '{' {
		s.pos++
	}
	durStr := strings.TrimSpace(s.src[durStart:s.pos])
	if !timeoutDurRe.MatchString(durStr) {
		s.pos = saved
		return false
	}

	goDuration := parseDurationToGo(durStr)

	s.skipWS()
	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		s.pos = saved
		return false
	}

	body, afterBody := extractBraceContent(s.src, s.pos)
	s.pos = afterBody

	// Look for on_timeout
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t' || s.src[s.pos] == '\n' || s.src[s.pos] == '\r') {
		s.pos++
	}

	var fallbackBody string
	if s.atWord("on_timeout") {
		s.skip(len("on_timeout"))
		s.skipWS()
		if s.pos < len(s.src) && s.src[s.pos] == '{' {
			fallbackBody, _ = extractBraceContent(s.src, s.pos)
			sc := &scanner{src: s.src}
			endPos := sc.findMatchingBrace(s.pos)
			if endPos != -1 {
				s.pos = endPos
			}
		}
	}

	var out strings.Builder
	out.WriteString("func() {\n")
	out.WriteString(fmt.Sprintf("\t__ctx, __cancel := context.WithTimeout(context.Background(), %s)\n", goDuration))
	out.WriteString("\tdefer __cancel()\n")
	out.WriteString("\t__done := make(chan struct{})\n")
	out.WriteString("\tgo func() {\n")
	out.WriteString("\t\tdefer close(__done)\n")
	out.WriteString(fmt.Sprintf("\t\t_ = __ctx\n"))
	out.WriteString(fmt.Sprintf("\t\t%s\n", strings.TrimSpace(body)))
	out.WriteString("\t}()\n")
	out.WriteString("\tselect {\n")
	out.WriteString("\tcase <-__done:\n")
	out.WriteString("\tcase <-__ctx.Done():\n")
	if fallbackBody != "" {
		out.WriteString(fmt.Sprintf("\t\t%s\n", strings.TrimSpace(fallbackBody)))
	}
	out.WriteString("\t}\n")
	out.WriteString("}()")

	s.emit(out.String())
	return true
}

func parseDurationToGo(dur string) string {
	m := timeoutDurRe.FindStringSubmatch(dur)
	if len(m) < 3 {
		return "0"
	}
	num := m[1]
	unit := m[2]
	switch unit {
	case "ms":
		return num + "*time.Millisecond"
	case "s":
		return num + "*time.Second"
	case "m":
		return num + "*time.Minute"
	case "h":
		return num + "*time.Hour"
	}
	return "0"
}

// --- branch parsing ---

type branch struct {
	name string
	body string
}

var branchRe = regexp.MustCompile(`branch\s+"([^"]+)"\s*\{`)

func parseBranches(body string) []branch {
	var branches []branch

	matches := branchRe.FindAllStringSubmatchIndex(body, -1)
	for _, loc := range matches {
		name := body[loc[2]:loc[3]]
		bracePos := loc[1] - 1
		sc := &scanner{src: body}
		endPos := sc.findMatchingBrace(bracePos)
		if endPos == -1 {
			continue
		}
		branchBody := body[bracePos+1 : endPos-1]
		branches = append(branches, branch{name: name, body: branchBody})
	}

	return branches
}
