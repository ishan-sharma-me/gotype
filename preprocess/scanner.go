package preprocess

import (
	"strings"
	"unicode"
)

// scanner is a simple lexical scanner for finding effect constructs in .tg source.
// It does NOT do full Go parsing — it just identifies effect-specific syntax to rewrite.
type scanner struct {
	src    string
	pos    int
	output strings.Builder
}

func newScanner(src string) *scanner {
	return &scanner{src: src}
}

// peek returns the next byte without advancing.
func (s *scanner) peek() byte {
	if s.pos >= len(s.src) {
		return 0
	}
	return s.src[s.pos]
}

// advance moves forward by n bytes, writing to output.
func (s *scanner) advance(n int) {
	end := s.pos + n
	if end > len(s.src) {
		end = len(s.src)
	}
	s.output.WriteString(s.src[s.pos:end])
	s.pos = end
}

// skip moves forward by n bytes WITHOUT writing to output.
func (s *scanner) skip(n int) {
	s.pos += n
	if s.pos > len(s.src) {
		s.pos = len(s.src)
	}
}

// emit writes a string to the output.
func (s *scanner) emit(str string) {
	s.output.WriteString(str)
}

// remaining returns the unprocessed source.
func (s *scanner) remaining() string {
	if s.pos >= len(s.src) {
		return ""
	}
	return s.src[s.pos:]
}

// atWord checks if the scanner is at the given word (not just a prefix of a longer identifier).
func (s *scanner) atWord(word string) bool {
	rem := s.remaining()
	if !strings.HasPrefix(rem, word) {
		return false
	}
	if len(rem) > len(word) {
		next := rune(rem[len(word)])
		if unicode.IsLetter(next) || unicode.IsDigit(next) || next == '_' {
			return false
		}
	}
	return true
}

// atWordBOL checks if at beginning of a logical line (after whitespace) with the given word.
// Used for top-level constructs like "effect".
func (s *scanner) atTopLevelWord(word string) bool {
	if !s.atWord(word) {
		return false
	}
	// Check that we're at the start of a line (only whitespace before us on this line)
	for i := s.pos - 1; i >= 0; i-- {
		ch := s.src[i]
		if ch == '\n' {
			return true
		}
		if ch != ' ' && ch != '\t' {
			return false
		}
	}
	return true // beginning of file
}

// skipString skips over a string literal (double-quoted or backtick).
func (s *scanner) skipString() {
	quote := s.src[s.pos]
	s.advance(1) // opening quote
	for s.pos < len(s.src) {
		ch := s.src[s.pos]
		if ch == '\\' && quote == '"' {
			s.advance(2) // escape sequence
			continue
		}
		if ch == quote {
			s.advance(1) // closing quote
			return
		}
		s.advance(1)
	}
}

// skipLineComment skips a // comment.
func (s *scanner) skipLineComment() {
	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		s.advance(1)
	}
}

// skipBlockComment skips a /* */ comment.
func (s *scanner) skipBlockComment() {
	s.advance(2) // /*
	for s.pos+1 < len(s.src) {
		if s.src[s.pos] == '*' && s.src[s.pos+1] == '/' {
			s.advance(2)
			return
		}
		s.advance(1)
	}
}

// readIdent reads an identifier starting at the current position.
func (s *scanner) readIdent() string {
	start := s.pos
	for s.pos < len(s.src) {
		ch := rune(s.src[s.pos])
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			s.pos++
		} else {
			break
		}
	}
	return s.src[start:s.pos]
}

// readIdentAt reads an identifier without advancing the scanner.
func (s *scanner) readIdentAt(pos int) string {
	start := pos
	for pos < len(s.src) {
		ch := rune(s.src[pos])
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			pos++
		} else {
			break
		}
	}
	return s.src[start:pos]
}

// skipWhitespace skips spaces and tabs (not newlines).
func (s *scanner) skipWS() {
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
}

// findMatchingBrace finds the closing brace matching the opening brace at pos.
// Returns the position after the closing brace, or -1 if not found.
// Handles nested braces, strings, and comments.
func (s *scanner) findMatchingBrace(pos int) int {
	if pos >= len(s.src) || s.src[pos] != '{' {
		return -1
	}
	depth := 0
	for pos < len(s.src) {
		ch := s.src[pos]
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return pos + 1
			}
		case '"':
			pos++
			for pos < len(s.src) {
				if s.src[pos] == '\\' {
					pos++ // skip escaped char
				} else if s.src[pos] == '"' {
					break
				}
				pos++
			}
		case '`':
			pos++
			for pos < len(s.src) && s.src[pos] != '`' {
				pos++
			}
		case '/':
			if pos+1 < len(s.src) {
				if s.src[pos+1] == '/' {
					for pos < len(s.src) && s.src[pos] != '\n' {
						pos++
					}
					continue
				}
				if s.src[pos+1] == '*' {
					pos += 2
					for pos+1 < len(s.src) {
						if s.src[pos] == '*' && s.src[pos+1] == '/' {
							pos++
							break
						}
						pos++
					}
				}
			}
		case '\'':
			pos++
			for pos < len(s.src) {
				if s.src[pos] == '\\' {
					pos++
				} else if s.src[pos] == '\'' {
					break
				}
				pos++
			}
		}
		pos++
	}
	return -1
}

// extractBraceContent extracts the content between { and }, starting at a position
// where src[pos] == '{'. Returns the inner content and the position after '}'.
func extractBraceContent(src string, pos int) (string, int) {
	if pos >= len(src) || src[pos] != '{' {
		return "", pos
	}
	depth := 0
	start := pos + 1
	for i := pos; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:i], i + 1
			}
		case '"':
			i++
			for i < len(src) {
				if src[i] == '\\' {
					i++
				} else if src[i] == '"' {
					break
				}
				i++
			}
		case '`':
			i++
			for i < len(src) && src[i] != '`' {
				i++
			}
		}
	}
	return src[start:], len(src)
}
