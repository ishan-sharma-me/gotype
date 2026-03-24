package preprocess

import (
	"regexp"
	"strings"
)

var reconcileBlockRe = regexp.MustCompile(`(?m)^reconcile\s+\w+\s*\{`)

// StripReconcileBlocks removes reconcile blocks from source before transpilation.
// Reconcile blocks are metadata for the daemon, not runtime code.
// Only strips blocks that are NOT inside comments.
func StripReconcileBlocks(src string) string {
	// First check if there are any reconcile blocks outside comments
	stripped := stripComments(src)
	if !reconcileBlockRe.MatchString(stripped) {
		return src
	}

	// Find and remove reconcile blocks that are not inside comments
	for {
		loc := reconcileBlockRe.FindStringIndex(src)
		if loc == nil {
			break
		}

		// Check if this match is inside a block comment
		if isInsideBlockComment(src, loc[0]) {
			break
		}

		bracePos := strings.Index(src[loc[0]:], "{")
		if bracePos == -1 {
			break
		}
		bracePos += loc[0]

		sc := &scanner{src: src}
		endPos := sc.findMatchingBrace(bracePos)
		if endPos == -1 {
			break
		}
		end := endPos
		for end < len(src) && (src[end] == '\n' || src[end] == '\r') {
			end++
		}
		src = src[:loc[0]] + src[end:]
	}
	return src
}

// isInsideBlockComment checks if position pos is inside a /* */ comment.
func isInsideBlockComment(src string, pos int) bool {
	inBlock := false
	for i := 0; i < pos && i < len(src)-1; i++ {
		if !inBlock && src[i] == '/' && src[i+1] == '*' {
			inBlock = true
			i++
		} else if inBlock && src[i] == '*' && src[i+1] == '/' {
			inBlock = false
			i++
		}
	}
	return inBlock
}
