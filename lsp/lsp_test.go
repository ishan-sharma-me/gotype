package lsp

import (
	"bufio"
	"bytes"
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteMessage(t *testing.T) {
	var buf bytes.Buffer
	msg := []byte(`{"jsonrpc":"2.0","method":"test"}`)

	if err := WriteMessage(&buf, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	reader := bufio.NewReader(&buf)
	got, err := ReadMessage(reader)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	if string(got) != string(msg) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, msg)
	}
}

func TestReadWriteMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	msgs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`,
	}

	for _, m := range msgs {
		WriteMessage(&buf, []byte(m))
	}

	reader := bufio.NewReader(&buf)
	for _, want := range msgs {
		got, err := ReadMessage(reader)
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestDocumentStore(t *testing.T) {
	ds := NewDocumentStore()

	ds.Open("file:///test.tg", 1, "package main")

	doc, ok := ds.Get("file:///test.tg")
	if !ok {
		t.Fatal("document not found after Open")
	}
	if doc.Content != "package main" {
		t.Errorf("content = %q, want %q", doc.Content, "package main")
	}

	ds.Update("file:///test.tg", 2, "package main\n\nfunc main() {}")
	doc, _ = ds.Get("file:///test.tg")
	if doc.Version != 2 {
		t.Errorf("version = %d, want 2", doc.Version)
	}

	ds.Close("file:///test.tg")
	_, ok = ds.Get("file:///test.tg")
	if ok {
		t.Error("document found after Close")
	}
}

func TestShadowProject(t *testing.T) {
	// Create a temp workspace
	wsDir := t.TempDir()
	os.WriteFile(filepath.Join(wsDir, "go.mod"), []byte("module test\n\ngo 1.23\n"), 0o644)

	sp := NewShadowProject(wsDir)
	if err := sp.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer sp.Cleanup()

	// Verify go.mod was copied
	shadowMod := filepath.Join(sp.Dir(), "go.mod")
	if _, err := os.Stat(shadowMod); err != nil {
		t.Error("go.mod not copied to shadow")
	}

	// Sync a valid .tg file
	tgPath := filepath.Join(wsDir, "main.tg")
	uri := pathToURI(tgPath)
	content := "package main\n\nfunc main() {}\n"

	shadowURI, diags := sp.SyncFile(uri, content)
	if len(diags) > 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	if shadowURI == "" {
		t.Fatal("shadowURI is empty")
	}

	// Verify .go file exists in shadow
	shadowPath := uriToPath(shadowURI)
	if !strings.HasSuffix(shadowPath, ".go") {
		t.Errorf("shadow path should end in .go: %s", shadowPath)
	}
	if _, err := os.Stat(shadowPath); err != nil {
		t.Errorf("shadow file not found: %v", err)
	}

	// Sync an invalid .tg file
	_, diags = sp.SyncFile(uri, "not valid go {{{")
	if len(diags) == 0 {
		t.Error("expected diagnostics for invalid Go")
	}
	if diags[0].Severity != SeverityError {
		t.Errorf("expected error severity, got %d", diags[0].Severity)
	}
}

func TestShadowURITranslation(t *testing.T) {
	sp := NewShadowProject("/workspace/myproject")

	tgURI := pathToURI("/workspace/myproject/pkg/user.tg")
	shadowURI := sp.TgToShadow(tgURI)

	if !strings.Contains(shadowURI, sp.Dir()) {
		t.Errorf("shadow URI should contain shadow dir: %s", shadowURI)
	}
	if !strings.HasSuffix(uriToPath(shadowURI), ".go") {
		t.Errorf("shadow path should end in .go: %s", shadowURI)
	}
}

func TestURIConversion(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"/Users/test/file.tg"},
		{"/tmp/workspace/main.go"},
	}

	for _, tt := range tests {
		uri := pathToURI(tt.path)
		if !strings.HasPrefix(uri, "file://") {
			t.Errorf("pathToURI(%q) = %q, missing file:// prefix", tt.path, uri)
		}
		got := uriToPath(uri)
		if got != tt.path {
			t.Errorf("roundtrip: uriToPath(pathToURI(%q)) = %q", tt.path, got)
		}
	}
}

func TestEffectCompletions(t *testing.T) {
	// Should match "perform" when typing "per"
	items := effectCompletions("per")
	found := false
	for _, item := range items {
		if item.Label == "perform" {
			found = true
			break
		}
	}
	if !found {
		t.Error("'perform' not in completions for prefix 'per'")
	}

	// Should match "effect" when typing "eff"
	items = effectCompletions("eff")
	found = false
	for _, item := range items {
		if item.Label == "effect" {
			found = true
			break
		}
	}
	if !found {
		t.Error("'effect' not in completions for prefix 'eff'")
	}

	// Empty prefix should return all keywords
	items = effectCompletions("")
	if len(items) != len(effectKeywords) {
		t.Errorf("empty prefix returned %d items, want %d", len(items), len(effectKeywords))
	}
}

func TestGetLinePrefix(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}"

	prefix := getLinePrefix(content, Position{Line: 0, Character: 7})
	if prefix != "package" {
		t.Errorf("got %q, want %q", prefix, "package")
	}

	prefix = getLinePrefix(content, Position{Line: 3, Character: 5})
	if prefix != "\tfmt." {
		t.Errorf("got %q, want %q", prefix, "\tfmt.")
	}
}

func TestParseDiagnostics(t *testing.T) {
	// Use go/parser directly to generate a real scanner.ErrorList
	_, err := goparser.ParseFile(token.NewFileSet(), "test.go", "not valid { go", goparser.AllErrors)
	if err == nil {
		t.Fatal("expected parse error")
	}

	diags := parseDiagnostics(err)
	if len(diags) == 0 {
		t.Fatal("expected diagnostics from parse error")
	}
	for _, d := range diags {
		if d.Source != "gotype" {
			t.Errorf("diagnostic source = %q, want %q", d.Source, "gotype")
		}
		if d.Severity != SeverityError {
			t.Errorf("diagnostic severity = %d, want %d", d.Severity, SeverityError)
		}
	}
}

func TestParseDiagnosticsNil(t *testing.T) {
	diags := parseDiagnostics(nil)
	if len(diags) != 0 {
		t.Error("nil error should produce no diagnostics")
	}
}
