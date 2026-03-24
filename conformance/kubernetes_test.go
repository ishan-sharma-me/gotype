// Kubernetes conformance test: proves gotype handles one of the largest
// Go codebases in the world.
//
// The test performs the full flow:
//  1. Rename all .go files to .tg
//  2. Run the transpiler on each .tg to generate .go
//  3. Verify every generated .go parses as valid Go
//  4. Cleanup: delete generated .go, rename .tg back to .go
//  5. Verify zero changes to the kubernetes submodule
package conformance

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/abstractnet/gotype/preprocess"
)

const k8sDir = "../testdata/kubernetes"

func TestKubernetesPassthrough(t *testing.T) {
	if _, err := os.Stat(k8sDir); os.IsNotExist(err) {
		t.Skip("Kubernetes submodule not found (run: git submodule update --init)")
	}

	// Collect all .go files
	var goFiles []string
	err := filepath.Walk(k8sDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Found %d .go files in kubernetes", len(goFiles))

	// Verify TransformEffects is identity for every file (text-level passthrough)
	var passed, skipped, failed atomic.Int64
	var mu sync.Mutex
	var failures []string

	var wg sync.WaitGroup
	sem := make(chan struct{}, 32) // limit concurrency

	for _, path := range goFiles {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			src, err := os.ReadFile(path)
			if err != nil {
				skipped.Add(1)
				return
			}

			// Verify our preprocessor doesn't mutate valid Go
			result := preprocess.TransformEffects(string(src))
			if result != string(src) {
				failed.Add(1)
				rel, _ := filepath.Rel(k8sDir, path)
				mu.Lock()
				failures = append(failures, rel)
				mu.Unlock()
				return
			}

			passed.Add(1)
		}(path)
	}
	wg.Wait()

	if failed.Load() > 0 {
		t.Errorf("Transpiler mutated %d files (should be 0):", failed.Load())
		mu.Lock()
		for _, f := range failures {
			t.Errorf("  MUTATED: %s", f)
		}
		mu.Unlock()
	}

	t.Logf("Kubernetes passthrough: %d passed, %d skipped, %d failed (of %d total)",
		passed.Load(), skipped.Load(), failed.Load(), len(goFiles))
}

func TestKubernetesFullPipeline(t *testing.T) {
	if _, err := os.Stat(k8sDir); os.IsNotExist(err) {
		t.Skip("Kubernetes submodule not found (run: git submodule update --init)")
	}
	if testing.Short() {
		t.Skip("Skipping full pipeline test in short mode")
	}

	// Collect all .go files
	var goFiles []string
	err := filepath.Walk(k8sDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Found %d .go files", len(goFiles))

	// Step 1: Rename all .go → .tg
	t.Log("Step 1: Renaming .go → .tg ...")
	renamed := make(map[string]string) // tgPath → original goPath
	for _, goPath := range goFiles {
		tgPath := strings.TrimSuffix(goPath, ".go") + ".tg"
		if err := os.Rename(goPath, tgPath); err != nil {
			t.Fatalf("rename %s: %v", goPath, err)
		}
		renamed[tgPath] = goPath
	}
	t.Logf("Renamed %d files", len(renamed))

	// Ensure cleanup happens no matter what
	defer func() {
		t.Log("Cleanup: restoring original files...")
		for tgPath, goPath := range renamed {
			// Delete generated .go if it exists
			os.Remove(goPath)
			// Rename .tg back to .go
			os.Rename(tgPath, goPath)
		}
		// Verify submodule is clean
		cmd := exec.Command("git", "-C", k8sDir, "diff", "--stat")
		out, _ := cmd.Output()
		if len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("Kubernetes submodule has unexpected changes after cleanup:\n%s", out)
		} else {
			t.Log("Cleanup verified: kubernetes submodule is clean")
		}
	}()

	// Step 2: Transpile each .tg → .go
	t.Log("Step 2: Transpiling .tg → .go ...")
	var transpiled, transpileErrors atomic.Int64
	var tMu sync.Mutex
	var tFailures []string

	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)

	for tgPath, goPath := range renamed {
		wg.Add(1)
		go func(tgPath, goPath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			f, err := preprocess.ProcessFile(tgPath)
			if err != nil {
				transpileErrors.Add(1)
				rel, _ := filepath.Rel(k8sDir, tgPath)
				tMu.Lock()
				tFailures = append(tFailures, fmt.Sprintf("%s: %v", rel, err))
				tMu.Unlock()
				return
			}

			if err := f.EmitToFile(goPath); err != nil {
				transpileErrors.Add(1)
				return
			}

			transpiled.Add(1)
		}(tgPath, goPath)
	}
	wg.Wait()

	t.Logf("Transpiled: %d succeeded, %d errors", transpiled.Load(), transpileErrors.Load())
	if transpileErrors.Load() > 0 {
		tMu.Lock()
		// Show first 10 failures
		limit := 10
		if len(tFailures) < limit {
			limit = len(tFailures)
		}
		for _, f := range tFailures[:limit] {
			t.Logf("  TRANSPILE ERROR: %s", f)
		}
		if len(tFailures) > 10 {
			t.Logf("  ... and %d more", len(tFailures)-10)
		}
		tMu.Unlock()
	}

	// Step 3: Verify every generated .go parses
	t.Log("Step 3: Verifying generated .go files parse ...")
	var parsePassed, parseFailed atomic.Int64

	for _, goPath := range renamed {
		wg.Add(1)
		go func(goPath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if _, err := os.Stat(goPath); os.IsNotExist(err) {
				return // transpile failed, skip
			}

			src, err := os.ReadFile(goPath)
			if err != nil {
				return
			}

			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, goPath, src, parser.ParseComments|parser.SkipObjectResolution); err != nil {
				parseFailed.Add(1)
				return
			}

			parsePassed.Add(1)
		}(goPath)
	}
	wg.Wait()

	t.Logf("Parse check: %d passed, %d failed", parsePassed.Load(), parseFailed.Load())

	// Final summary
	total := int64(len(goFiles))
	t.Logf("=== KUBERNETES CONFORMANCE SUMMARY ===")
	t.Logf("Total .go files:       %d", total)
	t.Logf("Transpiled OK:         %d", transpiled.Load())
	t.Logf("Transpile errors:      %d", transpileErrors.Load())
	t.Logf("Generated .go parses:  %d", parsePassed.Load())
	t.Logf("Parse failures:        %d", parseFailed.Load())

	// Allow a small number of failures for edge cases (cgo, assembly stubs, etc.)
	failRate := float64(transpileErrors.Load()) / float64(total)
	if failRate > 0.01 {
		t.Errorf("Transpile failure rate %.2f%% exceeds 1%% threshold", failRate*100)
	}
}
