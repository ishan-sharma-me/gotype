// Package daemon implements the Ananke daemon — file watching, module graph
// tracking, cascade test scheduling, and continuous validation.
package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches .tg files for changes with debouncing.
type Watcher struct {
	root     string
	debounce time.Duration
	fsw      *fsnotify.Watcher
	onChange func(files []string) // callback when files change

	mu      sync.Mutex
	pending map[string]time.Time
}

// NewWatcher creates a file watcher for the given root directory.
func NewWatcher(root string, onChange func(files []string)) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		root:     root,
		debounce: 100 * time.Millisecond,
		fsw:      fsw,
		onChange: onChange,
		pending:  make(map[string]time.Time),
	}

	// Walk and watch all directories containing .tg files
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			fsw.Add(path)
		}
		return nil
	})
	if err != nil {
		fsw.Close()
		return nil, err
	}

	return w, nil
}

// Run starts watching for changes. Blocks until the done channel is closed.
func (w *Watcher) Run(done <-chan struct{}) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".tg") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			w.mu.Lock()
			w.pending[event.Name] = time.Now()
			w.mu.Unlock()

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)

		case <-ticker.C:
			w.flush()

		case <-done:
			w.fsw.Close()
			return
		}
	}
}

// flush fires the onChange callback for files that have been stable past the debounce window.
func (w *Watcher) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	var ready []string

	for path, t := range w.pending {
		if now.Sub(t) >= w.debounce {
			ready = append(ready, path)
			delete(w.pending, path)
		}
	}

	if len(ready) > 0 && w.onChange != nil {
		w.onChange(ready)
	}
}

// Close shuts down the watcher.
func (w *Watcher) Close() {
	w.fsw.Close()
}
