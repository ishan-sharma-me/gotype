package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// GoplsProxy manages a gopls subprocess and proxies JSON-RPC requests to it.
type GoplsProxy struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex // protects stdin writes

	pending   map[int]chan json.RawMessage
	pendingMu sync.Mutex
	nextID    int

	shadowDir      string
	OnNotification func(method string, body []byte) // callback for gopls notifications
}

// NewGoplsProxy creates a new gopls proxy for the given shadow directory.
func NewGoplsProxy(shadowDir string) *GoplsProxy {
	return &GoplsProxy{
		shadowDir: shadowDir,
		pending:   make(map[int]chan json.RawMessage),
	}
}

// Start launches the gopls subprocess.
func (g *GoplsProxy) Start() error {
	g.cmd = exec.Command("gopls", "serve")
	g.cmd.Dir = g.shadowDir
	g.cmd.Stderr = os.Stderr

	var err error
	g.stdin, err = g.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("gopls stdin pipe: %w", err)
	}

	stdout, err := g.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gopls stdout pipe: %w", err)
	}
	g.stdout = bufio.NewReaderSize(stdout, 64*1024)

	if err := g.cmd.Start(); err != nil {
		return fmt.Errorf("starting gopls: %w (is gopls installed?)", err)
	}

	go g.readLoop()
	return nil
}

// readLoop reads JSON-RPC messages from gopls and dispatches them.
func (g *GoplsProxy) readLoop() {
	for {
		body, err := ReadMessage(g.stdout)
		if err != nil {
			if err != io.EOF {
				log.Printf("gopls read error: %v", err)
			}
			return
		}

		var msg struct {
			ID     *json.RawMessage `json:"id,omitempty"`
			Method string           `json:"method,omitempty"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		if msg.ID != nil && msg.Method == "" {
			// Response to a request we sent
			var id int
			if err := json.Unmarshal(*msg.ID, &id); err != nil {
				continue
			}
			g.pendingMu.Lock()
			ch, ok := g.pending[id]
			if ok {
				delete(g.pending, id)
			}
			g.pendingMu.Unlock()
			if ok {
				ch <- json.RawMessage(body)
			}
		} else if msg.Method != "" {
			// Notification from gopls
			if g.OnNotification != nil {
				g.OnNotification(msg.Method, body)
			}
		}
	}
}

// Call sends a JSON-RPC request to gopls and waits for the response.
func (g *GoplsProxy) Call(method string, params any) (json.RawMessage, error) {
	g.pendingMu.Lock()
	id := g.nextID
	g.nextID++
	ch := make(chan json.RawMessage, 1)
	g.pending[id] = ch
	g.pendingMu.Unlock()

	rawID := marshalRaw(id)
	req := Request{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  method,
		Params:  marshalRaw(params),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	err = WriteMessage(g.stdin, body)
	g.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("writing to gopls: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(10 * time.Second):
		g.pendingMu.Lock()
		delete(g.pending, id)
		g.pendingMu.Unlock()
		return nil, fmt.Errorf("gopls timeout: %s", method)
	}
}

// Notify sends a JSON-RPC notification to gopls (no response expected).
func (g *GoplsProxy) Notify(method string, params any) error {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  marshalRaw(params),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return WriteMessage(g.stdin, body)
}

// Initialize sends the initialize request to gopls.
func (g *GoplsProxy) Initialize(rootURI string) error {
	params := InitializeParams{
		RootURI: rootURI,
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				Completion: &CompletionClientCapabilities{
					CompletionItem: &CompletionItemCapabilities{
						SnippetSupport: true,
					},
				},
			},
		},
	}

	resp, err := g.Call("initialize", params)
	if err != nil {
		return fmt.Errorf("gopls initialize: %w", err)
	}
	_ = resp // we don't need gopls capabilities

	return g.Notify("initialized", struct{}{})
}

// Stop shuts down the gopls subprocess.
func (g *GoplsProxy) Stop() {
	if g.cmd == nil || g.cmd.Process == nil {
		return
	}

	// Try graceful shutdown
	g.Call("shutdown", nil)
	g.Notify("exit", nil)

	done := make(chan struct{})
	go func() {
		g.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		g.cmd.Process.Kill()
	}
}

// Running returns true if gopls is running.
func (g *GoplsProxy) Running() bool {
	return g.cmd != nil && g.cmd.Process != nil && g.cmd.ProcessState == nil
}
