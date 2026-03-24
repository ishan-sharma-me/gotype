package preprocess

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessSource_ValidGo(t *testing.T) {
	src := []byte(`package main

import "fmt"

func main() {
	fmt.Println("Hello from .tg!")
}
`)

	f, err := ProcessSource("test.tg", src)
	if err != nil {
		t.Fatalf("ProcessSource failed: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Emit(&buf); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "package main") {
		t.Error("output missing 'package main'")
	}
	if !strings.Contains(output, `fmt.Println("Hello from .tg!")`) {
		t.Error("output missing Println call")
	}
}

func TestProcessSource_InvalidGo(t *testing.T) {
	src := []byte(`this is not valid Go`)
	_, err := ProcessSource("bad.tg", src)
	if err == nil {
		t.Fatal("expected error for invalid Go source")
	}
}

func TestProcessFile(t *testing.T) {
	dir := t.TempDir()
	tgFile := filepath.Join(dir, "hello.tg")

	src := []byte(`package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`)
	if err := os.WriteFile(tgFile, src, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := ProcessFile(tgFile)
	if err != nil {
		t.Fatalf("ProcessFile failed: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Emit(&buf); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	if !strings.Contains(buf.String(), "package main") {
		t.Error("output missing 'package main'")
	}
}

func TestEmitToFile(t *testing.T) {
	src := []byte(`package main

func main() {}
`)
	f, err := ProcessSource("test.tg", src)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "test.go")
	if err := f.EmitToFile(outPath); err != nil {
		t.Fatalf("EmitToFile failed: %v", err)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "package main") {
		t.Error("written file missing 'package main'")
	}
}

func TestOutputPath(t *testing.T) {
	tests := []struct {
		tgPath string
		outDir string
		want   string
	}{
		{"hello.tg", "", "hello.go"},
		{"src/main.tg", "", "src/main.go"},
		{"main.tg", "gen", "gen/main.go"},
		{"src/main.tg", "out", "out/main.go"},
		{"noext", "", "noext"}, // no .tg extension, unchanged
	}
	for _, tt := range tests {
		got := OutputPath(tt.tgPath, tt.outDir)
		if got != tt.want {
			t.Errorf("OutputPath(%q, %q) = %q, want %q", tt.tgPath, tt.outDir, got, tt.want)
		}
	}
}
