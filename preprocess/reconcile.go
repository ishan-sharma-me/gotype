package preprocess

import (
	"regexp"
	"strings"
)

var reconcileBlockRe = regexp.MustCompile(`(?m)^reconcile\s+\w+\s*\{`)

// StripReconcileBlocks removes reconcile blocks from source before transpilation.
// Reconcile blocks are metadata for the daemon, not runtime code.
func StripReconcileBlocks(src string) string {
	for {
		loc := reconcileBlockRe.FindStringIndex(src)
		if loc == nil {
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
