package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abstractnet/gotype/preprocess"
)

// TestResult holds the outcome of running a test.
type TestResult struct {
	Module   string        `json:"module"`
	Test     string        `json:"test"`
	Status   string        `json:"status"` // "pass", "fail", "error"
	Duration time.Duration `json:"duration"`
	Output   string        `json:"output,omitempty"`
}

// Runner transpiles and runs tests for affected modules.
type Runner struct {
	root string
}

// NewRunner creates a test runner for the given project root.
func NewRunner(root string) *Runner {
	return &Runner{root: root}
}

// RunTests transpiles .tg files to a temp directory and runs their tests.
// Returns results for each test that ran.
func (r *Runner) RunTests(files []string) ([]TestResult, error) {
	if len(files) == 0 {
		return nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "gotyped-test-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Transpile each file
	for _, tgFile := range files {
		absPath := tgFile
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(r.root, tgFile)
		}

		src, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		content := string(src)
		hasTests := false
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "test ") && strings.Contains(line, `"`) {
				hasTests = true
				break
			}
		}

		f, err := preprocess.ProcessFile(absPath)
		if err != nil {
			return []TestResult{{
				Module: tgFile,
				Status: "error",
				Output: err.Error(),
			}}, nil
		}

		base := filepath.Base(tgFile)
		base = strings.TrimSuffix(base, ".tg")
		var goFile string
		if hasTests {
			goFile = filepath.Join(tmpDir, base+"_test.go")
		} else {
			goFile = filepath.Join(tmpDir, base+".go")
		}

		if err := f.EmitToFile(goFile); err != nil {
			continue
		}
	}

	// Create go.mod
	modContent := "module gotyped_test\n\ngo 1.23\n"
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(modContent), 0o644)

	// Run go test -json
	goCmd := exec.Command("go", "test", "-json", "-v", "./...")
	goCmd.Dir = tmpDir
	output, err := goCmd.CombinedOutput()

	return parseTestJSON(output, err), nil
}

// goTestEvent is the JSON structure go test -json emits.
type goTestEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Output  string    `json:"Output"`
	Elapsed float64   `json:"Elapsed"`
}

func parseTestJSON(output []byte, runErr error) []TestResult {
	var results []TestResult
	outputs := make(map[string]string) // test name → accumulated output

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var event goTestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.Test == "" {
			continue // package-level event
		}

		switch event.Action {
		case "output":
			outputs[event.Test] += event.Output
		case "pass":
			results = append(results, TestResult{
				Test:     event.Test,
				Status:   "pass",
				Duration: time.Duration(event.Elapsed * float64(time.Second)),
				Output:   outputs[event.Test],
			})
		case "fail":
			results = append(results, TestResult{
				Test:     event.Test,
				Status:   "fail",
				Duration: time.Duration(event.Elapsed * float64(time.Second)),
				Output:   outputs[event.Test],
			})
		}
	}

	// If no JSON events parsed but there was an error, report it
	if len(results) == 0 && runErr != nil {
		results = append(results, TestResult{
			Status: "error",
			Output: string(output),
		})
	}

	return results
}
