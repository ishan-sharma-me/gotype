// Package preprocess transforms .tg source files into valid Go source.
// It rewrites effect syntax (effect, perform, try/handle, resume) into
// valid Go code using the runtime package before go/parser sees it.
package preprocess

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"strings"
)

// File represents a preprocessed .tg file ready for Go compilation.
type File struct {
	Fset    *token.FileSet
	ASTFile *ast.File
}

// ProcessFile reads a .tg file and parses it as Go source.
// Returns the parsed AST and fileset for further processing.
func ProcessFile(filename string) (*File, error) {
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filename, err)
	}
	return ProcessSource(filename, src)
}

// ProcessSource preprocesses .tg source and parses it as Go.
// Effect and test syntax is transformed into valid Go before parsing.
func ProcessSource(filename string, src []byte) (*File, error) {
	transformed := TransformEffects(string(src))
	transformed = TransformTests(transformed)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, transformed, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	return &File{Fset: fset, ASTFile: f}, nil
}

// Emit writes the AST as Go source to the given writer.
func (f *File) Emit(w io.Writer) error {
	cfg := &printer.Config{
		Mode:     printer.UseSpaces | printer.TabIndent,
		Tabwidth: 8,
	}
	return cfg.Fprint(w, f.Fset, f.ASTFile)
}

// EmitToFile writes the AST as Go source to a file.
// The output filename is derived from the input by changing .tg to .go.
func (f *File) EmitToFile(outputPath string) error {
	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outputPath, err)
	}
	defer out.Close()
	if err := f.Emit(out); err != nil {
		return fmt.Errorf("writing %s: %w", outputPath, err)
	}
	return nil
}

// OutputPath converts a .tg filename to a .go filename in the given output directory.
// If outDir is empty, the .go file is placed next to the .tg file.
func OutputPath(tgPath string, outDir string) string {
	base := tgPath
	if strings.HasSuffix(base, ".tg") {
		base = strings.TrimSuffix(base, ".tg") + ".go"
	}
	if outDir != "" {
		// Extract just the filename part
		parts := strings.Split(base, "/")
		base = outDir + "/" + parts[len(parts)-1]
	}
	return base
}
