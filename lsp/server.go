package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// Server is the gotype LSP server.
type Server struct {
	reader *bufio.Reader
	writer io.Writer
	mu     sync.Mutex // protects writer

	docs    *DocumentStore
	shadow  *ShadowProject
	gopls   *GoplsProxy
	rootURI string

	// Track which .tg files gopls knows about (have been didOpen'd)
	goplsOpened   map[string]bool
	goplsOpenedMu sync.Mutex

	shutdown bool
}

// NewServer creates a new LSP server reading from r and writing to w.
func NewServer(r io.Reader, w io.Writer) *Server {
	return &Server{
		reader:      bufio.NewReaderSize(r, 64*1024),
		writer:      w,
		docs:        NewDocumentStore(),
		goplsOpened: make(map[string]bool),
	}
}

// Run starts the server's main loop.
func (s *Server) Run() error {
	for {
		body, err := ReadMessage(s.reader)
		if err != nil {
			if s.shutdown {
				return nil
			}
			return fmt.Errorf("reading message: %w", err)
		}

		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			log.Printf("invalid JSON-RPC: %v", err)
			continue
		}

		s.handle(req)
	}
}

func (s *Server) handle(req Request) {
	switch req.Method {
	case "initialize":
		s.handleRequest(req, s.handleInitialize)
	case "initialized":
		// no-op
	case "shutdown":
		s.shutdown = true
		s.respond(req.ID, nil, nil)
	case "exit":
		if s.gopls != nil {
			s.gopls.Stop()
		}
		if s.shadow != nil {
			s.shadow.Cleanup()
		}
		os.Exit(0)

	case "textDocument/didOpen":
		s.handleDidOpen(req)
	case "textDocument/didChange":
		s.handleDidChange(req)
	case "textDocument/didSave":
		s.handleDidSave(req)
	case "textDocument/didClose":
		s.handleDidClose(req)

	case "textDocument/completion":
		s.handleRequest(req, s.handleCompletion)
	case "textDocument/hover":
		s.handleRequest(req, s.proxyToGopls("textDocument/hover"))
	case "textDocument/definition":
		s.handleRequest(req, s.proxyToGopls("textDocument/definition"))
	case "textDocument/references":
		s.handleRequest(req, s.proxyToGopls("textDocument/references"))
	case "textDocument/formatting":
		s.handleRequest(req, s.proxyToGopls("textDocument/formatting"))
	case "textDocument/signatureHelp":
		s.handleRequest(req, s.proxyToGopls("textDocument/signatureHelp"))

	default:
		if !req.IsNotification() {
			// Unknown request — return method not found
			s.respond(req.ID, nil, &ResponseError{
				Code:    -32601,
				Message: "method not found: " + req.Method,
			})
		}
	}
}

type requestHandler func(req Request) (any, error)

func (s *Server) handleRequest(req Request, handler requestHandler) {
	result, err := handler(req)
	if err != nil {
		s.respond(req.ID, nil, &ResponseError{Code: -32603, Message: err.Error()})
		return
	}
	s.respond(req.ID, result, nil)
}

func (s *Server) handleInitialize(req Request) (any, error) {
	var params InitializeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, err
	}

	s.rootURI = params.RootURI
	rootDir := uriToPath(s.rootURI)

	// Set up shadow project
	s.shadow = NewShadowProject(rootDir)
	if err := s.shadow.Init(); err != nil {
		log.Printf("shadow init warning: %v", err)
	}

	// Start gopls
	s.gopls = NewGoplsProxy(s.shadow.Dir())
	s.gopls.OnNotification = s.handleGoplsNotification
	if err := s.gopls.Start(); err != nil {
		log.Printf("gopls start warning: %v (Go completions/hover disabled)", err)
		s.gopls = nil
	} else {
		shadowRootURI := pathToURI(s.shadow.Dir())
		if err := s.gopls.Initialize(shadowRootURI); err != nil {
			log.Printf("gopls initialize warning: %v", err)
		}
	}

	return InitializeResult{
		Capabilities: ServerCapabilities{
			TextDocumentSync: &TextDocumentSyncOptions{
				OpenClose: true,
				Change:    1, // Full sync
				Save:      &SaveOptions{IncludeText: true},
			},
			CompletionProvider: &CompletionOptions{
				TriggerCharacters: []string{".", "/"},
			},
			HoverProvider:      true,
			DefinitionProvider: true,
		},
		ServerInfo: &ServerInfo{
			Name:    "gotype-lsp",
			Version: "0.1.0",
		},
	}, nil
}

func (s *Server) handleDidOpen(req Request) {
	var params DidOpenTextDocumentParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return
	}

	uri := params.TextDocument.URI
	s.docs.Open(uri, params.TextDocument.Version, params.TextDocument.Text)
	s.syncAndDiagnose(uri, params.TextDocument.Text)
}

func (s *Server) handleDidChange(req Request) {
	var params DidChangeTextDocumentParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return
	}

	uri := params.TextDocument.URI
	if len(params.ContentChanges) == 0 {
		return
	}
	content := params.ContentChanges[len(params.ContentChanges)-1].Text
	s.docs.Update(uri, params.TextDocument.Version, content)
	s.syncAndDiagnose(uri, content)
}

func (s *Server) handleDidSave(req Request) {
	var params DidSaveTextDocumentParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return
	}

	if params.Text != nil {
		s.syncAndDiagnose(params.TextDocument.URI, *params.Text)
	}
}

func (s *Server) handleDidClose(req Request) {
	var params DidCloseTextDocumentParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return
	}

	uri := params.TextDocument.URI
	s.docs.Close(uri)

	// Tell gopls
	shadowURI := s.shadow.TgToShadow(uri)
	s.goplsOpenedMu.Lock()
	wasOpen := s.goplsOpened[shadowURI]
	delete(s.goplsOpened, shadowURI)
	s.goplsOpenedMu.Unlock()

	if wasOpen && s.gopls != nil {
		s.gopls.Notify("textDocument/didClose", DidCloseTextDocumentParams{
			TextDocument: TextDocumentIdentifier{URI: shadowURI},
		})
	}

	// Clear diagnostics
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: []Diagnostic{},
	})
}

// syncAndDiagnose preprocesses a .tg file, updates the shadow, sends diagnostics,
// and notifies gopls about the change.
func (s *Server) syncAndDiagnose(uri string, content string) {
	if s.shadow == nil {
		return
	}

	shadowURI, diags := s.shadow.SyncFile(uri, content)

	// Publish our parse diagnostics
	s.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})

	// Notify gopls about the shadow file
	if shadowURI != "" && s.gopls != nil && s.gopls.Running() {
		shadowContent, err := os.ReadFile(uriToPath(shadowURI))
		if err != nil {
			return
		}

		s.goplsOpenedMu.Lock()
		alreadyOpen := s.goplsOpened[shadowURI]
		s.goplsOpened[shadowURI] = true
		s.goplsOpenedMu.Unlock()

		if !alreadyOpen {
			s.gopls.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
				TextDocument: TextDocumentItem{
					URI:        shadowURI,
					LanguageID: "go",
					Version:    1,
					Text:       string(shadowContent),
				},
			})
		} else {
			s.gopls.Notify("textDocument/didChange", DidChangeTextDocumentParams{
				TextDocument: VersionedTextDocumentIdentifier{
					URI:     shadowURI,
					Version: 2,
				},
				ContentChanges: []TextDocumentContentChangeEvent{
					{Text: string(shadowContent)},
				},
			})
		}
	}
}

// proxyToGopls creates a handler that forwards requests to gopls with URI translation.
func (s *Server) proxyToGopls(method string) requestHandler {
	return func(req Request) (any, error) {
		if s.gopls == nil || !s.gopls.Running() {
			return nil, nil
		}

		// Translate URI in the request params
		translated := s.translateRequestURI(req.Params)

		resp, err := s.gopls.Call(method, json.RawMessage(translated))
		if err != nil {
			return nil, nil // silent fail — editor gets empty result
		}

		// Parse the response and translate URIs back
		var goplsResp Response
		if err := json.Unmarshal(resp, &goplsResp); err != nil {
			return nil, nil
		}
		if goplsResp.Error != nil {
			return nil, nil
		}

		// Translate shadow URIs back to .tg URIs in the result
		if goplsResp.Result != nil {
			resultBytes, _ := json.Marshal(goplsResp.Result)
			translated := s.translateResponseURIs(resultBytes)
			return json.RawMessage(translated), nil
		}

		return nil, nil
	}
}

// translateRequestURI replaces .tg URIs with shadow .go URIs in request params.
func (s *Server) translateRequestURI(params json.RawMessage) json.RawMessage {
	if s.shadow == nil {
		return params
	}
	str := string(params)
	rootPrefix := pathToURI(uriToPath(s.rootURI))
	shadowPrefix := pathToURI(s.shadow.Dir())

	// Replace workspace root with shadow root
	str = strings.ReplaceAll(str, rootPrefix, shadowPrefix)
	// Replace .tg extensions with .go
	str = strings.ReplaceAll(str, ".tg", ".go")

	return json.RawMessage(str)
}

// translateResponseURIs replaces shadow .go URIs back to .tg workspace URIs.
func (s *Server) translateResponseURIs(body []byte) []byte {
	if s.shadow == nil {
		return body
	}
	str := string(body)
	shadowPrefix := pathToURI(s.shadow.Dir())
	rootPrefix := pathToURI(uriToPath(s.rootURI))

	// Replace shadow dir with workspace root
	str = strings.ReplaceAll(str, shadowPrefix, rootPrefix)
	// Replace .go extensions back to .tg for files that were .tg
	// (This is a heuristic — only replace in file:// URIs)
	str = strings.ReplaceAll(str, ".go\"", ".tg\"")

	return []byte(str)
}

// handleGoplsNotification handles notifications from gopls (e.g., diagnostics).
func (s *Server) handleGoplsNotification(method string, body []byte) {
	switch method {
	case "textDocument/publishDiagnostics":
		// Translate shadow URIs back and forward to editor
		translated := s.translateResponseURIs(body)

		// Parse to extract the notification
		var notif Request
		if json.Unmarshal(translated, &notif) == nil {
			// Forward the translated notification
			s.mu.Lock()
			WriteMessage(s.writer, translated)
			s.mu.Unlock()
		}
	case "window/logMessage", "window/showMessage":
		// Forward directly
		s.mu.Lock()
		WriteMessage(s.writer, body)
		s.mu.Unlock()
	}
}

// respond sends a JSON-RPC response.
func (s *Server) respond(id *json.RawMessage, result any, respErr *ResponseError) {
	if id == nil {
		return // notifications don't get responses
	}
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   respErr,
	}
	body, err := json.Marshal(resp)
	if err != nil {
		log.Printf("marshal response error: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	WriteMessage(s.writer, body)
}

// notify sends a JSON-RPC notification to the client.
func (s *Server) notify(method string, params any) {
	req := Request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  marshalRaw(params),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	WriteMessage(s.writer, body)
}
