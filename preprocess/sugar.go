package preprocess

import (
	"fmt"
	"regexp"
	"strings"
)

// TransformSugar rewrites syntactic sugar into valid Go.
// Called AFTER TransformEffects and BEFORE TransformTests.
//
// Transforms:
//   x |> f |> g                                → g(f(x))
//   type Result = Success{v string} | Fail{e error}  → interface + structs
//   match expr { case Variant{bindings}: body }       → type switch
func TransformSugar(src string) string {
	if !containsSugarSyntax(src) {
		return src
	}

	src = transformSumTypes(src)
	src = transformMatch(src)
	src = transformPipeline(src)

	return src
}

func containsSugarSyntax(src string) bool {
	return strings.Contains(src, "|>") ||
		containsSumTypeDecl(src) ||
		containsMatchExpr(src)
}

// --- Pipeline operator ---
// x |> f |> g(extra) → g(f(x), extra)
// Handles: expr |> func |> func(args...)

func transformPipeline(src string) string {
	if !strings.Contains(src, "|>") {
		return src
	}

	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "|>") {
			continue
		}
		// Don't touch comments or strings (simple heuristic)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Find the assignment part and the expression part
		// e.g., "    result := data |> transform |> validate"
		eqIdx := strings.Index(line, ":=")
		if eqIdx == -1 {
			eqIdx = strings.Index(line, "= ")
		}

		if eqIdx != -1 {
			prefix := line[:eqIdx]
			// Find the operator (either := or =)
			op := ":="
			rest := line[eqIdx+2:]
			if line[eqIdx] == '=' && (eqIdx == 0 || line[eqIdx-1] != ':') {
				op = "="
				rest = line[eqIdx+1:]
			}
			rest = strings.TrimSpace(rest)

			if strings.Contains(rest, "|>") {
				lines[i] = prefix + op + " " + rewritePipeline(rest)
			}
		} else if strings.Contains(line, "|>") {
			// No assignment — standalone pipeline expression
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + rewritePipeline(trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

func findPipelineExprStart(src string, pipePos int) int {
	// Walk backwards from |> to find start of expression
	// Skip past whitespace, then find the start of the expression
	pos := pipePos - 1
	for pos >= 0 && (src[pos] == ' ' || src[pos] == '\t') {
		pos--
	}

	// Walk back past the expression (identifier, func call with parens, etc.)
	depth := 0
	for pos >= 0 {
		ch := src[pos]
		if ch == ')' || ch == ']' {
			depth++
		} else if ch == '(' || ch == '[' {
			depth--
			if depth < 0 {
				return pos + 1
			}
		} else if depth == 0 {
			if ch == '\n' || ch == ';' || ch == '=' || ch == ',' || ch == '{' {
				return pos + 1
			}
			if ch == ':' && pos > 0 && src[pos-1] == ':' {
				// := assignment
				return pos + 1
			}
		}
		pos--
	}
	return 0
}

func findPipelineExprEnd(src string, afterFirstPipe int) int {
	pos := afterFirstPipe
	for pos < len(src) {
		// Skip whitespace
		for pos < len(src) && (src[pos] == ' ' || src[pos] == '\t') {
			pos++
		}

		// Read a segment (identifier or func call)
		segStart := pos
		for pos < len(src) {
			ch := src[pos]
			if ch == '(' {
				// Skip over function call args
				depth := 1
				pos++
				for pos < len(src) && depth > 0 {
					if src[pos] == '(' {
						depth++
					} else if src[pos] == ')' {
						depth--
					}
					pos++
				}
				continue
			}
			if isIdentChar(rune(ch)) || ch == '.' {
				pos++
				continue
			}
			break
		}
		_ = segStart

		// Check if there's another |>
		tmpPos := pos
		for tmpPos < len(src) && (src[tmpPos] == ' ' || src[tmpPos] == '\t') {
			tmpPos++
		}
		if tmpPos+1 < len(src) && src[tmpPos] == '|' && src[tmpPos+1] == '>' {
			pos = tmpPos + 2
			continue
		}

		return pos
	}
	return pos
}

// rewritePipeline transforms "a |> f |> g(x)" into "g(f(a), x)"
func rewritePipeline(expr string) string {
	segments := splitPipeline(expr)
	if len(segments) < 2 {
		return expr
	}

	// Start with the first segment as the accumulator
	result := strings.TrimSpace(segments[0])

	for _, seg := range segments[1:] {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		// Check if segment has args: f(extra1, extra2)
		if idx := strings.Index(seg, "("); idx != -1 {
			funcName := seg[:idx]
			argsInner := seg[idx+1 : len(seg)-1] // strip parens
			if strings.TrimSpace(argsInner) == "" {
				result = fmt.Sprintf("%s(%s)", funcName, result)
			} else {
				result = fmt.Sprintf("%s(%s, %s)", funcName, result, argsInner)
			}
		} else {
			result = fmt.Sprintf("%s(%s)", seg, result)
		}
	}

	return result
}

func splitPipeline(expr string) []string {
	var segments []string
	depth := 0
	start := 0

	for i := 0; i < len(expr)-1; i++ {
		ch := expr[i]
		if ch == '(' || ch == '[' || ch == '{' {
			depth++
		} else if ch == ')' || ch == ']' || ch == '}' {
			depth--
		} else if depth == 0 && ch == '|' && expr[i+1] == '>' {
			segments = append(segments, expr[start:i])
			start = i + 2
		}
	}
	segments = append(segments, expr[start:])
	return segments
}

// --- Sum types ---
// type Result = Success{value string} | Failure{err error}
// →
// type Result interface { __isResult() }
// type Success struct { Value string }; func (Success) __isResult() {}
// type Failure struct { Err error }; func (Failure) __isResult() {}

var sumTypeRe = regexp.MustCompile(`(?m)^type\s+(\w+)\s*=\s*(.+\|.+)$`)

func containsSumTypeDecl(src string) bool {
	return sumTypeRe.MatchString(src)
}

func transformSumTypes(src string) string {
	return sumTypeRe.ReplaceAllStringFunc(src, func(match string) string {
		sub := sumTypeRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}

		typeName := sub[1]
		variantsStr := sub[2]

		variants := strings.Split(variantsStr, "|")
		sealMethod := fmt.Sprintf("__is%s", typeName)

		var out strings.Builder
		// Interface
		out.WriteString(fmt.Sprintf("type %s interface { %s() }\n", typeName, sealMethod))

		for _, v := range variants {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}

			// Parse: VariantName{field1 type1, field2 type2} or just VariantName
			variantName, fields := parseVariant(v)
			if variantName == "" {
				continue
			}

			// Struct
			if len(fields) == 0 {
				out.WriteString(fmt.Sprintf("type %s struct{}\n", variantName))
			} else {
				out.WriteString(fmt.Sprintf("type %s struct {\n", variantName))
				for _, f := range fields {
					// Keep the field name as declared by the user so struct literals match
					out.WriteString(fmt.Sprintf("\t%s %s\n", f.name, f.typ))
				}
				out.WriteString("}\n")
			}

			// Seal method
			out.WriteString(fmt.Sprintf("func (%s) %s() {}\n", variantName, sealMethod))
		}

		return out.String()
	})
}

func parseVariant(v string) (string, []paramDef) {
	braceIdx := strings.Index(v, "{")
	if braceIdx == -1 {
		return strings.TrimSpace(v), nil
	}

	name := strings.TrimSpace(v[:braceIdx])
	fieldsStr := v[braceIdx+1:]
	if idx := strings.LastIndex(fieldsStr, "}"); idx != -1 {
		fieldsStr = fieldsStr[:idx]
	}

	var fields []paramDef
	for _, f := range strings.Split(fieldsStr, ",") {
		f = strings.TrimSpace(f)
		parts := strings.Fields(f)
		if len(parts) == 2 {
			fields = append(fields, paramDef{name: parts[0], typ: parts[1]})
		} else if len(parts) == 1 {
			fields = append(fields, paramDef{name: parts[0]})
		}
	}

	return name, fields
}

// --- Match expressions ---
// match expr {
// case Success{v}:
//     body
// case Failure{e}:
//     body
// }
// →
// switch __m := (expr).(type) {
// case Success:
//     v := __m.V  (exported field)
//     body
// case Failure:
//     e := __m.E
//     body
// }

var matchStartRe = regexp.MustCompile(`(?m)match\s+`)

func containsMatchExpr(src string) bool {
	return matchStartRe.MatchString(src)
}

func transformMatch(src string) string {
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

		if s.atWord("match") {
			if s.transformMatchExpr() {
				continue
			}
		}

		s.advance(1)
	}
	return s.output.String()
}

func (s *scanner) transformMatchExpr() bool {
	saved := s.pos
	s.skip(len("match"))
	s.skipWS()

	// Read the expression to match (until '{')
	exprStart := s.pos
	for s.pos < len(s.src) && s.src[s.pos] != '{' {
		s.pos++
	}
	matchExpr := strings.TrimSpace(s.src[exprStart:s.pos])
	if matchExpr == "" {
		s.pos = saved
		return false
	}

	if s.pos >= len(s.src) {
		s.pos = saved
		return false
	}

	// Extract body
	body, afterBody := extractBraceContent(s.src, s.pos)
	s.pos = afterBody

	// Parse cases
	cases := parseMatchCases(body)
	if len(cases) == 0 {
		s.pos = saved
		return false
	}

	// Generate type switch
	var out strings.Builder
	out.WriteString(fmt.Sprintf("switch __m := (%s).(type) {\n", matchExpr))
	for _, c := range cases {
		out.WriteString(fmt.Sprintf("\tcase %s:\n", c.typeName))
		// Bind fields — use the field name as-is (matches struct declaration)
		for _, f := range c.bindings {
			out.WriteString(fmt.Sprintf("\t\t%s := __m.%s\n", f, f))
		}
		out.WriteString(fmt.Sprintf("\t\t%s\n", strings.TrimSpace(c.body)))
	}
	out.WriteString("\t}")

	s.emit(out.String())
	return true
}

type matchCase struct {
	typeName string
	bindings []string // field names to bind
	body     string
}

func parseMatchCases(body string) []matchCase {
	var cases []matchCase
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

		typeName := s.readIdent()
		if typeName == "" {
			continue
		}

		// Optional bindings: {v} or {v, e}
		var bindings []string
		if s.pos < len(s.src) && s.src[s.pos] == '{' {
			s.pos++
			for s.pos < len(s.src) && s.src[s.pos] != '}' {
				s.skipWS()
				if s.pos < len(s.src) && s.src[s.pos] == '}' {
					break
				}
				name := s.readIdent()
				if name != "" {
					bindings = append(bindings, name)
				}
				s.skipWS()
				if s.pos < len(s.src) && s.src[s.pos] == ',' {
					s.pos++
				}
			}
			if s.pos < len(s.src) {
				s.pos++ // skip }
			}
		}

		s.skipWS()
		if s.pos < len(s.src) && s.src[s.pos] == ':' {
			s.pos++
		}

		// Read body until next case or end
		bodyStart := s.pos
		for s.pos < len(s.src) {
			tmpPos := s.pos
			for tmpPos < len(s.src) && (s.src[tmpPos] == ' ' || s.src[tmpPos] == '\t' || s.src[tmpPos] == '\n' || s.src[tmpPos] == '\r') {
				tmpPos++
			}
			rem := s.src[tmpPos:]
			if strings.HasPrefix(rem, "case ") && len(rem) > 5 && (rem[4] == ' ' || rem[4] == '\t') {
				break
			}
			s.pos++
		}

		cases = append(cases, matchCase{
			typeName: typeName,
			bindings: bindings,
			body:     s.src[bodyStart:s.pos],
		})
	}

	return cases
}
