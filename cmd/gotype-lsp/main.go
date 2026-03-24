// gotype-lsp is the Language Server Protocol server for .tg files.
// It provides diagnostics, completions, hover, and go-to-definition
// by proxying to gopls with a shadow project.
package main

import (
	"log"
	"os"

	"github.com/abstractnet/gotype/lsp"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("gotype-lsp: ")

	s := lsp.NewServer(os.Stdin, os.Stdout)
	if err := s.Run(); err != nil {
		log.Fatal(err)
	}
}
