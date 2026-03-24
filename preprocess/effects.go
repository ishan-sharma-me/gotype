package preprocess

import (
	"fmt"
	"regexp"
	"strings"
)

// TransformEffects rewrites .tg effect syntax into plain Go using only
// goroutines and channels. NO runtime import — output is self-contained Go.
//
// Two-pass approach:
//   Pass 1: Find all functions that contain `perform` (effectful functions)
//   Pass 2: Inject __eff parameter into effectful funcs, add __eff arg at call sites,
//           transform effect syntax into channel operations
//
// The user writes clean code with NO plumbing:
//
//   func getName(user *User) string {
//       return perform AskName.(string)
//   }
//   handle { getName(arya) } with { case AskName: resume("Alice") }
//
// The transpiler handles all the wiring automatically.
func TransformEffects(src string) string {
	if !containsEffectSyntax(src) {
		return src
	}

	// Pass 1: find effectful functions (those containing `perform`)
	effectfulFuncs := findEffectfulFuncs(src)

	// Inject __effReq type after imports
	src = injectEffectType(src)

	// Inject __eff parameter into effectful function signatures
	src = injectEffParams(src, effectfulFuncs)

	// Inject __eff argument at call sites to effectful functions
	src = injectEffArgs(src, effectfulFuncs)

	// Pass 2: transform effect syntax
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

		if s.atTopLevelWord("effect") {
			if s.transformEffectDecl() {
				continue
			}
		}

		if s.atWord("handle") {
			if s.transformHandleWith() {
				continue
			}
		}

		if s.atWord("perform") {
			if s.transformPerform() {
				continue
			}
		}

		s.advance(1)
	}

	return s.output.String()
}

// containsEffectSyntax does a quick check for effect keywords that appear
// as actual statements (start of line or after whitespace), not inside
// comments or strings. This is a fast pre-filter — false positives are OK
// (the real scanner handles them), but false negatives would skip transformation.
var effectDeclRe = regexp.MustCompile(`(?m)^effect\s+[A-Z]`) // effect Name — effect names are capitalized

func containsEffectSyntax(src string) bool {
	stripped := stripComments(src)
	if effectDeclRe.MatchString(stripped) {
		return true
	}
	for _, line := range strings.Split(stripped, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "perform ") ||
			strings.HasPrefix(trimmed, "handle {") ||
			strings.HasPrefix(trimmed, "handle\t{") ||
			strings.Contains(trimmed, "= perform ") ||
			strings.Contains(trimmed, ":= perform ") {
			return true
		}
	}
	return false
}

// --- Pass 1: Find effectful functions ---

// funcDeclRe matches `func name(` at the start of a line (top-level function declarations)
var funcDeclRe = regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(`)

// findEffectfulFuncs returns the names of all functions that are effectful,
// either directly (contain `perform`) or transitively (call an effectful function).
func findEffectfulFuncs(src string) map[string]bool {
	// Step 1: Map each function to its body
	type funcInfo struct {
		name string
		body string
	}
	var funcs []funcInfo

	matches := funcDeclRe.FindAllStringIndex(src, -1)
	for _, loc := range matches {
		chunk := src[loc[0]:loc[1]]
		parts := strings.Fields(chunk)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSuffix(parts[1], "(")

		bodyStart := findFuncBodyStart(src, loc[1])
		if bodyStart == -1 {
			continue
		}

		sc := &scanner{src: src}
		bodyEnd := sc.findMatchingBrace(bodyStart)
		if bodyEnd == -1 {
			continue
		}

		funcs = append(funcs, funcInfo{name: name, body: src[bodyStart:bodyEnd]})
	}

	// Step 2: Seed with directly effectful functions
	effectful := make(map[string]bool)
	for _, f := range funcs {
		if strings.Contains(f.body, "perform ") {
			effectful[f.name] = true
		}
	}

	// Step 3: Propagate transitively — if a function calls an effectful function, it's effectful too
	changed := true
	for changed {
		changed = false
		for _, f := range funcs {
			if effectful[f.name] {
				continue
			}
			for callee := range effectful {
				// Check if this function's body calls the effectful function
				if containsCall(f.body, callee) {
					effectful[f.name] = true
					changed = true
					break
				}
			}
		}
	}

	// Exclude main — it uses handle/with, not __eff parameter
	delete(effectful, "main")

	return effectful
}

// containsCall checks if body contains a call to funcName (not just the string).
func containsCall(body, funcName string) bool {
	idx := 0
	for {
		pos := strings.Index(body[idx:], funcName)
		if pos == -1 {
			return false
		}
		absPos := idx + pos
		afterName := absPos + len(funcName)

		// Must be followed by '(' (possibly with whitespace)
		rest := strings.TrimSpace(body[afterName:])
		if len(rest) == 0 || rest[0] != '(' {
			idx = afterName
			continue
		}

		// Must not be preceded by identifier char (not part of larger name)
		if absPos > 0 && isIdentChar(rune(body[absPos-1])) {
			idx = afterName
			continue
		}

		return true
	}
}

// findFuncBodyStart finds the opening '{' of a function body starting from pos
// (which is right after the function name's '(').
func findFuncBodyStart(src string, pos int) int {
	depth := 1 // we're inside the param '('
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

// --- Inject __eff parameter into effectful functions ---

// injectEffParams adds `__eff chan<- __effReq` as the first parameter to effectful functions.
func injectEffParams(src string, effectful map[string]bool) string {
	if len(effectful) == 0 {
		return src
	}

	// Process in reverse order so string indices stay valid
	matches := funcDeclRe.FindAllStringIndex(src, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		loc := matches[i]
		chunk := src[loc[0]:loc[1]]
		parts := strings.Fields(chunk)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSuffix(parts[1], "(")
		if !effectful[name] {
			continue
		}

		// Insert __eff param right after the '('
		insertAt := loc[1] // position right after '('

		// Check if params are empty: func name()
		afterParen := strings.TrimSpace(src[insertAt:])
		if strings.HasPrefix(afterParen, ")") {
			src = src[:insertAt] + "__eff chan<- __effReq" + src[insertAt:]
		} else {
			src = src[:insertAt] + "__eff chan<- __effReq, " + src[insertAt:]
		}
	}

	return src
}

// --- Inject __eff argument at call sites ---

// injectEffArgs adds `__eff` as the first argument wherever an effectful function is called.
func injectEffArgs(src string, effectful map[string]bool) string {
	if len(effectful) == 0 {
		return src
	}

	for name := range effectful {
		// Match: name( — a call to this function
		// But NOT: func name( — that's the declaration
		// Use a simple approach: replace `name(` with `name(__eff, ` or `name(__eff)`
		// but skip the declaration line.

		callPattern := name + "("
		idx := 0
		for {
			pos := strings.Index(src[idx:], callPattern)
			if pos == -1 {
				break
			}
			absPos := idx + pos

			// Check if this is a declaration (preceded by "func ")
			before := src[max(0, absPos-10):absPos]
			if strings.HasSuffix(strings.TrimSpace(before), "func") {
				idx = absPos + len(callPattern)
				continue
			}

			// Check character before name — must not be letter/digit/underscore (not part of larger identifier)
			if absPos > 0 {
				prev := src[absPos-1]
				if isIdentChar(rune(prev)) {
					idx = absPos + len(callPattern)
					continue
				}
			}

			// Insert __eff as first argument
			insertAt := absPos + len(callPattern)
			afterParen := strings.TrimSpace(src[insertAt:])
			if strings.HasPrefix(afterParen, ")") {
				src = src[:insertAt] + "__eff" + src[insertAt:]
			} else {
				src = src[:insertAt] + "__eff, " + src[insertAt:]
			}

			idx = insertAt + 6 // skip past what we inserted
		}
	}

	return src
}

func isIdentChar(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

// --- Inject __effReq type ---

func injectEffectType(src string) string {
	typeDef := "\ntype __effReq struct{ name string; args []any; rch chan any }\n"

	if strings.Contains(src, "__effReq") && strings.Contains(src, "struct{") {
		return src
	}

	if idx := strings.Index(src, "import ("); idx != -1 {
		closeIdx := strings.Index(src[idx:], ")")
		if closeIdx != -1 {
			insertAt := idx + closeIdx + 1
			return src[:insertAt] + typeDef + src[insertAt:]
		}
	}

	if idx := strings.Index(src, "import "); idx != -1 {
		lineEnd := strings.Index(src[idx:], "\n")
		if lineEnd != -1 {
			insertAt := idx + lineEnd + 1
			return src[:insertAt] + typeDef + src[insertAt:]
		}
	}

	pkgEnd := strings.Index(src, "\n")
	if pkgEnd != -1 {
		return src[:pkgEnd+1] + typeDef + src[pkgEnd+1:]
	}
	return src + typeDef
}

// --- Transform effect syntax ---

func (s *scanner) transformEffectDecl() bool {
	saved := s.pos
	s.skip(len("effect"))
	s.skipWS()

	name := s.readIdent()
	if name == "" {
		s.pos = saved
		return false
	}

	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		s.pos++
	}

	s.emit(fmt.Sprintf("// __gotype_effect:%s", name))
	return true
}

func (s *scanner) transformPerform() bool {
	saved := s.pos
	s.skip(len("perform"))
	s.skipWS()

	name := s.readIdent()
	if name == "" {
		s.pos = saved
		return false
	}

	var argsExpr string
	if s.pos < len(s.src) && s.src[s.pos] == '(' {
		s.pos++
		depth := 1
		argsStart := s.pos
		for s.pos < len(s.src) && depth > 0 {
			switch s.src[s.pos] {
			case '(':
				depth++
			case ')':
				depth--
			case '"':
				s.pos++
				for s.pos < len(s.src) {
					if s.src[s.pos] == '\\' {
						s.pos++
					} else if s.src[s.pos] == '"' {
						break
					}
					s.pos++
				}
			case '`':
				s.pos++
				for s.pos < len(s.src) && s.src[s.pos] != '`' {
					s.pos++
				}
			}
			if depth > 0 {
				s.pos++
			}
		}
		args := strings.TrimSpace(s.src[argsStart:s.pos])
		s.pos++
		if args != "" {
			argsExpr = "[]any{" + args + "}"
		}
	}

	if argsExpr == "" {
		argsExpr = "nil"
	}

	s.emit(fmt.Sprintf(
		`func() any { __rch := make(chan any, 1); __eff <- __effReq{%q, %s, __rch}; return <-__rch }()`,
		name, argsExpr))

	return true
}

func (s *scanner) transformHandleWith() bool {
	saved := s.pos
	s.skip(len("handle"))
	s.skipWS()

	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		s.pos = saved
		return false
	}

	handleBody, afterHandle := extractBraceContent(s.src, s.pos)
	s.pos = afterHandle

	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t' || s.src[s.pos] == '\n' || s.src[s.pos] == '\r') {
		s.pos++
	}

	if !s.atWord("with") {
		s.pos = saved
		return false
	}
	s.skip(len("with"))
	s.skipWS()

	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		s.pos = saved
		return false
	}

	withBody, afterWith := extractBraceContent(s.src, s.pos)
	s.pos = afterWith

	switchCases := parseCaseClauses(withBody)

	// Transform perform/handle inside the body (without the full pipeline —
	// no type injection or param rewriting, just syntax transforms)
	transformedBody := transformBodySyntax(handleBody)

	s.emit(fmt.Sprintf(`func() {
	__eff := make(chan __effReq)
	__done := make(chan struct{})
	go func() {
		defer close(__done)
		%s
	}()
	for {
		select {
		case __req := <-__eff:
			switch __req.name {%s
			}
		case <-__done:
			return
		}
	}
}()`, transformedBody, switchCases))

	return true
}

func parseCaseClauses(body string) string {
	var cases []string
	s := newScanner(body)

	for s.pos < len(s.src) {
		for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t' || s.src[s.pos] == '\n' || s.src[s.pos] == '\r') {
			s.pos++
		}
		if s.pos >= len(s.src) {
			break
		}

		if !s.atWord("case") {
			s.pos++
			continue
		}
		s.skip(4)
		s.skipWS()

		effectName := s.readIdent()
		if effectName == "" {
			continue
		}

		var params []paramDef
		if s.pos < len(s.src) && s.src[s.pos] == '(' {
			s.pos++
			for s.pos < len(s.src) && s.src[s.pos] != ')' {
				s.skipWS()
				if s.pos < len(s.src) && s.src[s.pos] == ')' {
					break
				}
				pName := s.readIdent()
				s.skipWS()
				var pType string
				if s.pos < len(s.src) && s.src[s.pos] != ',' && s.src[s.pos] != ')' {
					pType = s.readIdent()
				}
				if pName != "" {
					params = append(params, paramDef{name: pName, typ: pType})
				}
				s.skipWS()
				if s.pos < len(s.src) && s.src[s.pos] == ',' {
					s.pos++
				}
			}
			if s.pos < len(s.src) {
				s.pos++
			}
		}

		s.skipWS()
		if s.pos < len(s.src) && s.src[s.pos] == ':' {
			s.pos++
		}

		bodyStart := s.pos
		for s.pos < len(s.src) {
			tmpPos := s.pos
			for tmpPos < len(s.src) && (s.src[tmpPos] == ' ' || s.src[tmpPos] == '\t' || s.src[tmpPos] == '\n' || s.src[tmpPos] == '\r') {
				tmpPos++
			}
			remaining := s.src[tmpPos:]
			if strings.HasPrefix(remaining, "case ") && len(remaining) > 5 {
				after := remaining[4]
				if after == ' ' || after == '\t' {
					break
				}
			}
			s.pos++
		}
		caseBody := s.src[bodyStart:s.pos]

		var argUnpack string
		for i, p := range params {
			if p.typ != "" {
				argUnpack += fmt.Sprintf("\n\t\t\t%s := __req.args[%d].(%s)", p.name, i, p.typ)
			} else {
				argUnpack += fmt.Sprintf("\n\t\t\t%s := __req.args[%d]", p.name, i)
			}
			if !strings.Contains(caseBody, p.name) {
				argUnpack += fmt.Sprintf("\n\t\t\t_ = %s", p.name)
			}
		}

		transformedBody := transformResume(caseBody)

		cases = append(cases, fmt.Sprintf("\n\t\t\tcase %q:%s%s", effectName, argUnpack, transformedBody))
	}

	return strings.Join(cases, "")
}

func transformResume(body string) string {
	s := newScanner(body)
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

		if s.atWord("resume") {
			s.skip(len("resume"))
			s.skipWS()

			if s.pos < len(s.src) && s.src[s.pos] == '(' {
				s.pos++
				depth := 1
				argsStart := s.pos
				for s.pos < len(s.src) && depth > 0 {
					switch s.src[s.pos] {
					case '(':
						depth++
					case ')':
						depth--
					}
					if depth > 0 {
						s.pos++
					}
				}
				args := strings.TrimSpace(s.src[argsStart:s.pos])
				s.pos++

				if args == "" {
					s.emit("__req.rch <- nil")
				} else {
					s.emit(fmt.Sprintf("__req.rch <- %s", args))
				}
			} else {
				s.emit("__req.rch <- nil")
			}
			continue
		}

		s.advance(1)
	}
	return s.output.String()
}

type paramDef struct {
	name string
	typ  string
}

// transformBodySyntax transforms perform and nested handle/with inside a body
// without running the full pipeline (no type injection, no param rewriting).
func transformBodySyntax(body string) string {
	s := newScanner(body)
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

		if s.atWord("handle") {
			if s.transformHandleWith() {
				continue
			}
		}

		if s.atWord("perform") {
			if s.transformPerform() {
				continue
			}
		}

		s.advance(1)
	}
	return s.output.String()
}
