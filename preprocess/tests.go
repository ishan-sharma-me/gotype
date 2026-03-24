package preprocess

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// TransformTests rewrites .tg test syntax into Go test functions.
// This is called AFTER TransformEffects so that handle/with/perform
// inside test blocks are already transformed.
//
// Transforms:
//   test "description" { body }  →  func TestDescription(t *testing.T) { body }
//   assert expr                  →  if !(expr) { t.Fatalf("assert failed: expr") }
func TransformTests(src string) string {
	if !containsTestSyntax(src) {
		return src
	}

	src = ensureTestingImport(src)
	src = transformTestBlocks(src)
	src = transformAsserts(src)

	return src
}

func containsTestSyntax(src string) bool {
	// Strip block comments before checking
	stripped := stripBlockComments(src)
	for _, line := range strings.Split(stripped, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "test ") && strings.Contains(trimmed, `"`) {
			return true
		}
	}
	return false
}

func stripBlockComments(src string) string {
	var out strings.Builder
	i := 0
	for i < len(src) {
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) {
				if src[i] == '*' && src[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		out.WriteByte(src[i])
		i++
	}
	return out.String()
}

func ensureTestingImport(src string) string {
	if strings.Contains(src, `"testing"`) {
		return src
	}

	if idx := strings.Index(src, "import ("); idx != -1 {
		insertAt := idx + len("import (")
		return src[:insertAt] + "\n\t\"testing\"" + src[insertAt:]
	}

	if idx := strings.Index(src, "import "); idx != -1 {
		lineEnd := strings.Index(src[idx:], "\n")
		if lineEnd != -1 {
			existingImport := strings.TrimSpace(src[idx+len("import ") : idx+lineEnd])
			newImport := fmt.Sprintf("import (\n\t%s\n\t\"testing\"\n)", existingImport)
			return src[:idx] + newImport + src[idx+lineEnd:]
		}
	}

	pkgEnd := strings.Index(src, "\n")
	if pkgEnd != -1 {
		return src[:pkgEnd+1] + "\nimport \"testing\"\n" + src[pkgEnd+1:]
	}
	return src + "\nimport \"testing\"\n"
}

// testBlockRe matches: test "description" {
var testBlockRe = regexp.MustCompile(`(?m)^test\s+"([^"]+)"\s*\{`)

func transformTestBlocks(src string) string {
	matches := testBlockRe.FindAllStringIndex(src, -1)
	if len(matches) == 0 {
		return src
	}

	// Process in reverse to keep indices valid
	for i := len(matches) - 1; i >= 0; i-- {
		loc := matches[i]

		// Extract the description
		submatch := testBlockRe.FindStringSubmatch(src[loc[0]:loc[1]])
		if len(submatch) < 2 {
			continue
		}
		desc := submatch[1]
		funcName := testNameToFuncName(desc)

		// Find the matching closing brace
		sc := &scanner{src: src}
		braceStart := loc[0] + strings.Index(src[loc[0]:loc[1]], "{")
		braceEnd := sc.findMatchingBrace(braceStart)
		if braceEnd == -1 {
			continue
		}

		body := src[braceStart+1 : braceEnd-1]

		// Replace the test block with a Go test function
		goFunc := fmt.Sprintf("func %s(t *testing.T) {%s}", funcName, body)
		src = src[:loc[0]] + goFunc + src[braceEnd:]
	}

	return src
}

// transformAsserts replaces `assert expr` with `if !(expr) { t.Fatalf(...) }`
func transformAsserts(src string) string {
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

		if s.atWord("assert") {
			// Check it's not part of a larger identifier
			if s.pos > 0 && isIdentChar(rune(s.src[s.pos-1])) {
				s.advance(1)
				continue
			}

			s.skip(len("assert"))
			s.skipWS()

			// Read the expression until end of line or semicolon
			exprStart := s.pos
			depth := 0
			for s.pos < len(s.src) {
				c := s.src[s.pos]
				if c == '(' || c == '[' || c == '{' {
					depth++
				} else if c == ')' || c == ']' || c == '}' {
					if depth == 0 {
						break
					}
					depth--
				} else if depth == 0 && (c == '\n' || c == ';') {
					break
				}
				s.pos++
			}
			expr := strings.TrimSpace(s.src[exprStart:s.pos])

			if expr != "" {
				// Escape the expression for use in format string
				escaped := strings.ReplaceAll(expr, `"`, `\"`)
				escaped = strings.ReplaceAll(escaped, "%", "%%")
				s.emit(fmt.Sprintf(`if !(%s) { t.Fatalf("assert failed: %s") }`, expr, escaped))
			}
			continue
		}

		s.advance(1)
	}
	return s.output.String()
}

// testNameToFuncName converts "getName returns prompted name" to "TestGetNameReturnsPromptedName"
func testNameToFuncName(desc string) string {
	var b strings.Builder
	b.WriteString("Test")

	capitalize := true
	for _, r := range desc {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if capitalize {
				b.WriteRune(unicode.ToUpper(r))
				capitalize = false
			} else {
				b.WriteRune(r)
			}
		} else {
			capitalize = true
		}
	}

	return b.String()
}
