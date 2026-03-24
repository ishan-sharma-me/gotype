// gotype is a transpiler that converts .tg files into .go files.
// .tg is a strict superset of Go that adds algebraic effects.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/abstractnet/gotype/preprocess"
)

const usage = `gotype - transpile .tg files to Go

Usage:
  gotype transpile <files...>    Transpile .tg files to .go
  gotype run <file.tg>           Transpile and run a .tg file
  gotype build <files...>        Transpile and build .tg files
  gotype test <files...>         Transpile and run test blocks
  gotype graph <files...>        Show module dependency graph
  gotype version                 Print version

Options:
  -o <dir>    Output directory for generated .go files (default: next to source)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "transpile":
		if len(args) == 0 {
			fatal("transpile requires at least one .tg file")
		}
		outDir := extractFlag(&args, "-o")
		if err := transpile(args, outDir); err != nil {
			fatal("%v", err)
		}

	case "run":
		if len(args) == 0 {
			fatal("run requires a .tg file")
		}
		if err := run(args[0], args[1:]); err != nil {
			fatal("%v", err)
		}

	case "build":
		if len(args) == 0 {
			fatal("build requires at least one .tg file")
		}
		outDir := extractFlag(&args, "-o")
		if err := build(args, outDir); err != nil {
			fatal("%v", err)
		}

	case "test":
		if len(args) == 0 {
			fatal("test requires at least one .tg file")
		}
		if err := runTests(args); err != nil {
			fatal("%v", err)
		}

	case "graph":
		if len(args) == 0 {
			fatal("graph requires at least one .tg file")
		}
		if err := showGraph(args); err != nil {
			fatal("%v", err)
		}

	case "check":
		if len(args) == 0 {
			fatal("check requires at least one .tg file")
		}
		hasErrors := false
		for _, f := range args {
			src, err := os.ReadFile(f)
			if err != nil {
				fatal("%v", err)
			}
			report := preprocess.CheckAndReport(string(src), f)
			if report != "" {
				fmt.Fprint(os.Stderr, report)
				hasErrors = true
			}
		}
		if hasErrors {
			os.Exit(1)
		}
		fmt.Println("No effect errors found.")

	case "version":
		fmt.Println("gotype v0.1.0-dev")

	case "help", "-h", "--help":
		fmt.Print(usage)

	default:
		// If arg ends with .tg, treat as implicit "run"
		if strings.HasSuffix(cmd, ".tg") {
			if err := run(cmd, args); err != nil {
				fatal("%v", err)
			}
		} else {
			fatal("unknown command: %s\nRun 'gotype help' for usage.", cmd)
		}
	}
}

// transpile converts .tg files to .go files.
func transpile(files []string, outDir string) error {
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("creating output directory: %w", err)
		}
	}

	for _, tgFile := range files {
		f, err := preprocess.ProcessFile(tgFile)
		if err != nil {
			return err
		}

		outPath := preprocess.OutputPath(tgFile, outDir)
		if err := f.EmitToFile(outPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s → %s\n", tgFile, outPath)
	}
	return nil
}

// run transpiles a .tg file to a temp directory and runs it with `go run`.
func run(tgFile string, extraArgs []string) error {
	tmpDir, err := os.MkdirTemp("", "gotype-run-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Transpile
	f, err := preprocess.ProcessFile(tgFile)
	if err != nil {
		return err
	}

	goFile := filepath.Join(tmpDir, "main.go")
	if err := f.EmitToFile(goFile); err != nil {
		return err
	}

	// Create a go.mod in the temp directory
	// Generated code is self-contained Go — no external imports needed
	modContent := "module gotype_run\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(modContent), 0o644); err != nil {
		return fmt.Errorf("writing go.mod: %w", err)
	}

	// Run with `go run`
	goArgs := append([]string{"run", goFile}, extraArgs...)
	goCmd := exec.Command("go", goArgs...)
	goCmd.Dir = tmpDir
	goCmd.Stdout = os.Stdout
	goCmd.Stderr = os.Stderr
	goCmd.Stdin = os.Stdin
	return goCmd.Run()
}

// build transpiles .tg files and runs `go build` on the output.
func build(files []string, outDir string) error {
	if outDir == "" {
		outDir = "."
	}
	if err := transpile(files, outDir); err != nil {
		return err
	}

	goCmd := exec.Command("go", "build", "./...")
	goCmd.Dir = outDir
	goCmd.Stdout = os.Stdout
	goCmd.Stderr = os.Stderr
	return goCmd.Run()
}

// runTests transpiles .tg files containing test blocks into _test.go and runs `go test`.
func runTests(tgFiles []string) error {
	tmpDir, err := os.MkdirTemp("", "gotype-test-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Transpile each file
	for _, tgFile := range tgFiles {
		src, err := os.ReadFile(tgFile)
		if err != nil {
			return fmt.Errorf("reading %s: %w", tgFile, err)
		}

		content := string(src)
		hasTests := false
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "test ") && strings.Contains(line, `"`) {
				hasTests = true
				break
			}
		}

		f, err := preprocess.ProcessFile(tgFile)
		if err != nil {
			return err
		}

		// Files with test blocks become _test.go, others become .go
		base := filepath.Base(tgFile)
		base = strings.TrimSuffix(base, ".tg")
		var goFile string
		if hasTests {
			goFile = filepath.Join(tmpDir, base+"_test.go")
		} else {
			goFile = filepath.Join(tmpDir, base+".go")
		}

		if err := f.EmitToFile(goFile); err != nil {
			return err
		}
	}

	// Create go.mod
	modContent := "module gotype_test\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(modContent), 0o644); err != nil {
		return fmt.Errorf("writing go.mod: %w", err)
	}

	// Run go test
	goCmd := exec.Command("go", "test", "-v", "./...")
	goCmd.Dir = tmpDir
	goCmd.Stdout = os.Stdout
	goCmd.Stderr = os.Stderr
	return goCmd.Run()
}

// showGraph prints the module dependency graph derived from effect declarations and perform calls.
func showGraph(tgFiles []string) error {
	type funcInfo struct {
		name     string
		file     string
		performs []string // effect names this function performs
	}

	var funcs []funcInfo
	effects := make(map[string]bool)

	for _, tgFile := range tgFiles {
		src, err := os.ReadFile(tgFile)
		if err != nil {
			return err
		}

		content := string(src)
		base := filepath.Base(tgFile)

		// Collect effect declarations
		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "effect ") {
				parts := strings.Fields(trimmed)
				if len(parts) >= 2 {
					name := strings.Split(parts[1], "(")[0]
					effects[name] = true
				}
			}
		}

		// Find functions and what they perform
		funcRe := regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(`)
		funcMatches := funcRe.FindAllStringSubmatchIndex(content, -1)

		for _, loc := range funcMatches {
			name := content[loc[2]:loc[3]]

			// Find function body
			bodyStart := findFuncBody(content, loc[1])
			if bodyStart == -1 {
				continue
			}
			sc := &simpleScanner{src: content}
			bodyEnd := sc.findBrace(bodyStart)
			if bodyEnd == -1 {
				continue
			}

			body := content[bodyStart:bodyEnd]

			// Find all perform calls in the body
			var performs []string
			performRe := regexp.MustCompile(`perform\s+(\w+)`)
			for _, m := range performRe.FindAllStringSubmatch(body, -1) {
				performs = append(performs, m[1])
			}

			if len(performs) > 0 {
				funcs = append(funcs, funcInfo{name: name, file: base, performs: performs})
			}
		}
	}

	// Print the graph
	if len(effects) > 0 {
		fmt.Println("Effects:")
		for e := range effects {
			fmt.Printf("  %s\n", e)
		}
		fmt.Println()
	}

	if len(funcs) > 0 {
		fmt.Println("Modules (functions that perform effects):")
		for _, f := range funcs {
			fmt.Printf("  %s (%s)\n", f.name, f.file)
			for _, p := range f.performs {
				fmt.Printf("    └── performs %s\n", p)
			}
		}
	}

	return nil
}

// findFuncBody finds the opening '{' of a function body
func findFuncBody(src string, pos int) int {
	depth := 1
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

// simpleScanner is a minimal scanner for brace matching
type simpleScanner struct{ src string }

func (s *simpleScanner) findBrace(pos int) int {
	if pos >= len(s.src) || s.src[pos] != '{' {
		return -1
	}
	depth := 0
	for pos < len(s.src) {
		switch s.src[pos] {
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
					pos++
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
		}
		pos++
	}
	return -1
}

// extractFlag removes a flag and its value from args, returning the value.
func extractFlag(args *[]string, flag string) string {
	for i, a := range *args {
		if a == flag && i+1 < len(*args) {
			val := (*args)[i+1]
			*args = append((*args)[:i], (*args)[i+2:]...)
			return val
		}
	}
	return ""
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gotype: "+format+"\n", args...)
	os.Exit(1)
}
