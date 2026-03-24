package lsp

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"go/scanner"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abstractnet/gotype/preprocess"
)

// ShadowProject maintains a shadow directory where .tg files are mirrored
// as preprocessed .go files. This is what gopls operates on.
type ShadowProject struct {
	rootDir   string // original workspace root
	shadowDir string // shadow directory path
	mu        sync.Mutex
	tgFiles   map[string]bool // tracks which shadow files originated from .tg
}

// NewShadowProject creates a shadow project for the given workspace root.
func NewShadowProject(rootDir string) *ShadowProject {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(rootDir)))[:16]
	shadowDir := filepath.Join(cacheDir(), "gotype-lsp", hash)

	sp := &ShadowProject{
		rootDir:   rootDir,
		shadowDir: shadowDir,
		tgFiles:   make(map[string]bool),
	}
	return sp
}

// Init creates the shadow directory and performs the initial mirror.
func (sp *ShadowProject) Init() error {
	if err := os.MkdirAll(sp.shadowDir, 0o755); err != nil {
		return fmt.Errorf("creating shadow dir: %w", err)
	}

	// Copy go.mod and go.sum if they exist
	for _, name := range []string{"go.mod", "go.sum"} {
		src := filepath.Join(sp.rootDir, name)
		dst := filepath.Join(sp.shadowDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			continue // file may not exist
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("copying %s: %w", name, err)
		}
	}

	// Walk workspace and mirror all .tg and .go files
	return filepath.Walk(sp.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			// Skip hidden dirs and common non-source dirs
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(sp.rootDir, path)
		if err != nil {
			return nil
		}

		switch {
		case strings.HasSuffix(path, ".tg"):
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			sp.syncTgContent(rel, string(data))
		case strings.HasSuffix(path, ".go"):
			// Copy .go files verbatim
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			dst := filepath.Join(sp.shadowDir, rel)
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, data, 0o644)
		}
		return nil
	})
}

// SyncFile preprocesses a .tg file and writes the .go result to the shadow dir.
// Returns the shadow URI and any parse diagnostics.
func (sp *ShadowProject) SyncFile(uri string, content string) (shadowURI string, diags []Diagnostic) {
	path := uriToPath(uri)
	if !strings.HasSuffix(path, ".tg") {
		return "", nil
	}

	rel, err := filepath.Rel(sp.rootDir, path)
	if err != nil {
		return "", []Diagnostic{{
			Range:    Range{},
			Severity: SeverityError,
			Source:   "gotype",
			Message:  fmt.Sprintf("file outside workspace: %s", path),
		}}
	}

	diags = sp.syncTgContent(rel, content)
	shadowRel := strings.TrimSuffix(rel, ".tg") + ".go"
	return pathToURI(filepath.Join(sp.shadowDir, shadowRel)), diags
}

// syncTgContent does the actual preprocess and write. rel is workspace-relative.
func (sp *ShadowProject) syncTgContent(rel string, content string) []Diagnostic {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	shadowRel := strings.TrimSuffix(rel, ".tg") + ".go"
	shadowPath := filepath.Join(sp.shadowDir, shadowRel)

	f, err := preprocess.ProcessSource(rel, []byte(content))
	if err != nil {
		// Return diagnostics but don't update shadow (gopls keeps last valid version)
		return parseDiagnostics(err)
	}

	// Write preprocessed Go to shadow
	os.MkdirAll(filepath.Dir(shadowPath), 0o755)
	if err := f.EmitToFile(shadowPath); err != nil {
		return []Diagnostic{{
			Range:    Range{},
			Severity: SeverityError,
			Source:   "gotype",
			Message:  fmt.Sprintf("writing shadow file: %v", err),
		}}
	}

	sp.tgFiles[shadowRel] = true
	return nil
}

// RemoveFile removes a file from the shadow directory.
func (sp *ShadowProject) RemoveFile(uri string) {
	path := uriToPath(uri)
	rel, err := filepath.Rel(sp.rootDir, path)
	if err != nil {
		return
	}
	shadowRel := strings.TrimSuffix(rel, ".tg") + ".go"
	shadowPath := filepath.Join(sp.shadowDir, shadowRel)

	sp.mu.Lock()
	defer sp.mu.Unlock()
	os.Remove(shadowPath)
	delete(sp.tgFiles, shadowRel)
}

// TgToShadow converts a .tg workspace URI to its .go shadow URI.
func (sp *ShadowProject) TgToShadow(tgURI string) string {
	path := uriToPath(tgURI)
	rel, err := filepath.Rel(sp.rootDir, path)
	if err != nil {
		return tgURI
	}
	rel = strings.TrimSuffix(rel, ".tg") + ".go"
	return pathToURI(filepath.Join(sp.shadowDir, rel))
}

// ShadowToTg converts a .go shadow URI back to the .tg workspace URI.
func (sp *ShadowProject) ShadowToTg(shadowURI string) string {
	path := uriToPath(shadowURI)
	rel, err := filepath.Rel(sp.shadowDir, path)
	if err != nil {
		return shadowURI
	}

	sp.mu.Lock()
	isTg := sp.tgFiles[rel]
	sp.mu.Unlock()

	if !isTg {
		// This was a .go file, map it back to workspace but keep .go extension
		return pathToURI(filepath.Join(sp.rootDir, rel))
	}

	rel = strings.TrimSuffix(rel, ".go") + ".tg"
	return pathToURI(filepath.Join(sp.rootDir, rel))
}

// Dir returns the shadow directory path.
func (sp *ShadowProject) Dir() string {
	return sp.shadowDir
}

// Cleanup removes the shadow directory.
func (sp *ShadowProject) Cleanup() {
	os.RemoveAll(sp.shadowDir)
}

// parseDiagnostics extracts structured diagnostics from Go parse errors.
func parseDiagnostics(err error) []Diagnostic {
	if err == nil {
		return nil
	}

	var errList scanner.ErrorList
	if errors.As(err, &errList) {
		diags := make([]Diagnostic, 0, len(errList))
		for _, e := range errList {
			diags = append(diags, Diagnostic{
				Range: Range{
					Start: Position{Line: e.Pos.Line - 1, Character: e.Pos.Column - 1},
					End:   Position{Line: e.Pos.Line - 1, Character: e.Pos.Column - 1},
				},
				Severity: SeverityError,
				Source:   "gotype",
				Message:  e.Msg,
			})
		}
		return diags
	}

	// Fallback: single diagnostic from error string
	return []Diagnostic{{
		Range:    Range{Start: Position{0, 0}, End: Position{0, 0}},
		Severity: SeverityError,
		Source:   "gotype",
		Message:  err.Error(),
	}}
}

// cacheDir returns the user's cache directory.
func cacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return dir
}
