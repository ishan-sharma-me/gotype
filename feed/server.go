package feed

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/abstractnet/gotype/daemon"
)

// Server is the HTTP/SSE server that streams daemon events to AI consumers.
type Server struct {
	addr   string
	daemon *daemon.Daemon

	mu      sync.Mutex
	clients map[chan []byte]bool
}

// NewServer creates a feed server bound to the given daemon.
func NewServer(addr string, d *daemon.Daemon) *Server {
	return &Server{
		addr:    addr,
		daemon:  d,
		clients: make(map[chan []byte]bool),
	}
}

// Broadcast sends an SSE event to all connected clients.
func (s *Server) Broadcast(event SSEEvent) {
	data := MarshalSSE(event)

	s.mu.Lock()
	defer s.mu.Unlock()

	for ch := range s.clients {
		select {
		case ch <- data:
		default:
			// Client too slow, drop event
		}
	}
}

// HandleDaemonEvent converts a daemon event to SSE and broadcasts it.
func (s *Server) HandleDaemonEvent(e daemon.Event) {
	s.Broadcast(DaemonEventToSSE(e))
}

// Start begins serving HTTP. Blocks until the server is shut down.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/stream", s.handleStream)
	mux.HandleFunc("/api/v1/state", s.handleState)
	mux.HandleFunc("/api/v1/modules", s.handleModules)
	mux.HandleFunc("/api/v1/graph", s.handleGraph)
	mux.HandleFunc("/api/v1/run", s.handleRun)
	mux.HandleFunc("/health", s.handleHealth)

	log.Printf("Feed server listening on %s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// StartHeartbeat sends periodic status summaries to all clients.
func (s *Server) StartHeartbeat(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.Broadcast(SSEEvent{
				Type: EventHeartbeat,
				Data: s.buildHeartbeat(),
			})
		case <-done:
			return
		}
	}
}

func (s *Server) buildHeartbeat() HeartbeatData {
	hb := HeartbeatData{Timestamp: time.Now()}

	d := s.daemon
	d.GraphData().Mu().RLock()
	defer d.GraphData().Mu().RUnlock()

	hb.Effects = len(d.GraphData().Effects)
	hb.Modules = len(d.GraphData().Modules)
	for _, m := range d.GraphData().Modules {
		switch m.State {
		case daemon.StateHealthy:
			hb.Healthy++
		case daemon.StateFailing:
			hb.Failing++
		case daemon.StateStale, daemon.StateTainted:
			hb.Stale++
		}
	}

	return hb
}

// --- SSE endpoint ---

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan []byte, 64)

	s.mu.Lock()
	s.clients[ch] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	// Send initial state
	state := s.buildHeartbeat()
	initial := MarshalSSE(SSEEvent{Type: EventHeartbeat, Data: state})
	w.Write(initial)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case data := <-ch:
			w.Write(data)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// --- REST endpoints ---

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": s.daemon.Status(),
		"graph":  s.buildGraphInfo(),
	})
}

func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	g := s.daemon.GraphData()
	g.Mu().RLock()
	defer g.Mu().RUnlock()

	var modules []ModuleInfo
	for _, m := range g.Modules {
		modules = append(modules, ModuleInfo{
			Name:     m.Name,
			File:     m.File,
			Performs: m.Performs,
			State:    m.State.String(),
		})
	}

	json.NewEncoder(w).Encode(modules)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.buildGraphInfo())
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Trigger a full test run
	go s.daemon.TriggerFullRun()

	json.NewEncoder(w).Encode(map[string]string{
		"status": "triggered",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) buildGraphInfo() GraphInfo {
	g := s.daemon.GraphData()
	g.Mu().RLock()
	defer g.Mu().RUnlock()

	var info GraphInfo
	for _, e := range g.Effects {
		info.Effects = append(info.Effects, EffectInfo{Name: e.Name, File: e.File})
	}
	for _, m := range g.Modules {
		info.Modules = append(info.Modules, ModuleInfo{
			Name:     m.Name,
			File:     m.File,
			Performs: m.Performs,
			State:    m.State.String(),
		})
	}
	return info
}

// Addr returns the server's listen address.
func (s *Server) Addr() string {
	return fmt.Sprintf("http://%s", s.addr)
}
