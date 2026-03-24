// Package feed provides an HTTP/SSE server for streaming daemon events
// to AI consumers. The AI never needs to manually run tests — the feed
// pushes everything in real-time.
package feed

import (
	"encoding/json"
	"time"

	"github.com/abstractnet/gotype/daemon"
)

// SSE event type names.
const (
	EventFileChanged  = "file_changed"
	EventTestStarted  = "test_started"
	EventTestComplete = "test_completed"
	EventGraphChanged = "graph_changed"
	EventHeartbeat    = "heartbeat"
)

// SSEEvent is a Server-Sent Event ready for serialization.
type SSEEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// FileChangedData is sent when .tg files are modified.
type FileChangedData struct {
	Files     []string  `json:"files"`
	Timestamp time.Time `json:"timestamp"`
}

// TestStartedData is sent when tests begin for a module.
type TestStartedData struct {
	Module    string    `json:"module"`
	Timestamp time.Time `json:"timestamp"`
}

// TestCompletedData is sent when test results are available.
type TestCompletedData struct {
	Results   []daemon.TestResult `json:"results"`
	Timestamp time.Time           `json:"timestamp"`
}

// HeartbeatData is sent periodically with a status summary.
type HeartbeatData struct {
	Effects  int       `json:"effects"`
	Modules  int       `json:"modules"`
	Healthy  int       `json:"healthy"`
	Failing  int       `json:"failing"`
	Stale    int       `json:"stale"`
	Timestamp time.Time `json:"timestamp"`
}

// ModuleInfo is returned by the /modules REST endpoint.
type ModuleInfo struct {
	Name     string   `json:"name"`
	File     string   `json:"file"`
	Performs []string `json:"performs"`
	State    string   `json:"state"`
	LastRun  string   `json:"last_run,omitempty"`
	LastErr  string   `json:"last_err,omitempty"`
}

// GraphInfo is returned by the /graph REST endpoint.
type GraphInfo struct {
	Effects []EffectInfo `json:"effects"`
	Modules []ModuleInfo `json:"modules"`
}

// EffectInfo describes an effect declaration.
type EffectInfo struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// DaemonEventToSSE converts a daemon event to an SSE event.
func DaemonEventToSSE(e daemon.Event) SSEEvent {
	switch e.Kind {
	case daemon.EventFileChanged:
		return SSEEvent{Type: EventFileChanged, Data: FileChangedData{
			Files: e.Files, Timestamp: e.Timestamp,
		}}
	case daemon.EventTestStarted:
		return SSEEvent{Type: EventTestStarted, Data: TestStartedData{
			Module: e.Module, Timestamp: e.Timestamp,
		}}
	case daemon.EventTestCompleted:
		return SSEEvent{Type: EventTestComplete, Data: TestCompletedData{
			Results: e.Results, Timestamp: e.Timestamp,
		}}
	case daemon.EventGraphChanged:
		return SSEEvent{Type: EventGraphChanged, Data: map[string]any{
			"timestamp": e.Timestamp,
		}}
	default:
		return SSEEvent{Type: "unknown", Data: e}
	}
}

// MarshalSSE formats an SSEEvent as a Server-Sent Events text block.
func MarshalSSE(event SSEEvent) []byte {
	data, _ := json.Marshal(event.Data)
	return []byte("event: " + event.Type + "\ndata: " + string(data) + "\n\n")
}
