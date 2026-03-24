// Package conformance validates that gotype is a strict superset of Go.
// Every valid .go file must pass through the transpiler unchanged.
//
// It uses the Go compiler's own test suite (testdata/go-spec/test/) as the
// conformance corpus — 3000+ files covering every Go language feature.
//
// For each .go file we:
//  1. Read the source
//  2. Run it through TransformEffects (should be identity — no effect syntax)
//  3. Verify the output is still valid Go (parses without error)
//  4. Verify the output is identical to the input (no accidental mutations)
package conformance

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abstractnet/gotype/preprocess"
)

const goSpecDir = "../testdata/go-spec/test"

// TestGoSpecPassthrough verifies that every Go spec test file passes through
// the transpiler without modification. This is the foundational guarantee
// that .tg is a strict superset of Go.
func TestGoSpecPassthrough(t *testing.T) {
	if _, err := os.Stat(goSpecDir); os.IsNotExist(err) {
		t.Skip("Go spec test suite not found (run: git submodule update --init)")
	}

	var total, passed, skipped, failed int

	err := filepath.Walk(goSpecDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip directories
		if info.IsDir() {
			name := info.Name()
			// Skip hidden dirs, vendor, etc
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Only process .go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		total++

		// Skip known problematic categories
		rel, _ := filepath.Rel(goSpecDir, path)
		if shouldSkip(rel) {
			skipped++
			return nil
		}

		// Read source
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Skip files that don't parse as valid Go to begin with
		// (assembly-related, build-tagged for other OS, etc.)
		fset := token.NewFileSet()
		if _, parseErr := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution); parseErr != nil {
			skipped++
			return nil
		}

		t.Run(rel, func(t *testing.T) {
			// Run through our preprocessor
			result := preprocess.TransformEffects(string(src))

			// Verify output is identical (no accidental mutations)
			if result != string(src) {
				t.Errorf("transpiler mutated non-effect Go source")
				// Show first difference
				for i := 0; i < len(result) && i < len(src); i++ {
					if result[i] != src[i] {
						start := max(0, i-20)
						end := min(len(result), i+20)
						t.Errorf("first diff at byte %d:\n  got:  %q\n  want: %q", i, result[start:end], string(src[start:min(len(src), i+20)]))
						break
					}
				}
				return
			}

			// Verify output still parses
			fset2 := token.NewFileSet()
			if _, parseErr := parser.ParseFile(fset2, path, result, parser.ParseComments|parser.SkipObjectResolution); parseErr != nil {
				t.Errorf("transpiled output doesn't parse: %v", parseErr)
				return
			}
		})

		passed++
		return nil
	})

	if err != nil {
		t.Fatalf("walk error: %v", err)
	}

	t.Logf("Go spec conformance: %d total, %d passed, %d skipped, %d failed",
		total, passed, skipped, failed)
}

// shouldSkip returns true for files that are known to need special handling
// and aren't relevant to our passthrough guarantee.
func shouldSkip(rel string) bool {
	// Multi-file test directories (*.dir/) — these need special compilation
	if strings.Contains(rel, ".dir/") || strings.Contains(rel, ".dir\\") {
		return true
	}

	// Assembly files aren't Go source
	if strings.HasSuffix(rel, "_test.go") {
		// Go test files with test functions are fine
		return false
	}

	return false
}
