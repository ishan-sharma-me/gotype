// gotyped is the Ananke daemon — watches .tg files, tracks the module
// dependency graph, runs tests with cascade invalidation, and streams
// events to AI consumers via SSE.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/abstractnet/gotype/daemon"
	"github.com/abstractnet/gotype/feed"
)

const defaultPort = "9876"

const usage = `gotyped (Ananke) - continuous test runner & reconciliation engine

Usage:
  gotyped start [path]                 Start daemon (dev: file watcher + tests)
  gotyped start --mode=prod [path]     Start daemon (prod: reconciliation on schedule)
  gotyped status [path]                Show module status summary
  gotyped reconcile [path]             Show reconciliation status
  gotyped graph [path]                 Print module dependency graph
  gotyped simulate --fail Effect       Simulate effect failure + cascade
  gotyped simulate --drift Effect.inv  Simulate invariant drift + cascade
  gotyped feed [addr]                  Connect to SSE feed and print events
  gotyped version                      Print version

SSE feed: http://localhost:9876/api/v1/stream
`

func main() {
	log.SetPrefix("gotyped: ")
	log.SetFlags(log.Ltime)

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	arg := "."
	if len(os.Args) > 2 {
		arg = os.Args[2]
	}

	// Check for --mode=prod flag
	isProd := false
	for i, a := range os.Args {
		if a == "--mode=prod" {
			isProd = true
			// Remove the flag from args
			os.Args = append(os.Args[:i], os.Args[i+1:]...)
			break
		}
	}
	// Recalculate arg after flag removal
	if len(os.Args) > 2 {
		arg = os.Args[2]
	}

	switch cmd {
	case "start":
		if isProd {
			if err := startProd(arg); err != nil {
				log.Fatal(err)
			}
		} else {
			if err := start(arg); err != nil {
				log.Fatal(err)
			}
		}
	case "status":
		if err := status(arg); err != nil {
			log.Fatal(err)
		}
	case "reconcile":
		if err := reconcileStatus(arg); err != nil {
			log.Fatal(err)
		}
	case "graph":
		if err := graph(arg); err != nil {
			log.Fatal(err)
		}
	case "simulate":
		if err := simulate(); err != nil {
			log.Fatal(err)
		}
	case "feed":
		addr := "localhost:" + defaultPort
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		if err := connectFeed(addr); err != nil {
			log.Fatal(err)
		}
	case "version":
		fmt.Println("gotyped v0.1.0-dev (Ananke)")
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func start(root string) error {
	d, err := daemon.New(root)
	if err != nil {
		return err
	}

	// Create feed server
	addr := ":" + defaultPort
	srv := feed.NewServer(addr, d)

	// Wire daemon events to both feed server and stdout
	d.OnEventFunc(func(e daemon.Event) {
		srv.HandleDaemonEvent(e)
	})

	// Handle Ctrl+C
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("Shutting down...")
		close(done)
		d.Stop()
	}()

	// Start feed server in background
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("Feed server error: %v", err)
		}
	}()

	// Start heartbeat
	go srv.StartHeartbeat(5*time.Second, done)

	log.Printf("Feed: http://localhost:%s/api/v1/stream", defaultPort)

	return d.Run()
}

func startProd(root string) error {
	d, err := daemon.New(root)
	if err != nil {
		return err
	}

	// Build module graph
	if err := d.RunOnce(); err != nil {
		return err
	}

	// Create reconciler
	recon := daemon.NewReconciler(root, d.GraphData())
	if err := recon.Scan(); err != nil {
		return err
	}

	// Create feed server
	addr := ":" + defaultPort
	srv := feed.NewServer(addr, d)

	// Wire reconciliation events to feed
	recon.OnEventFunc(func(e daemon.ReconcileEvent) {
		srv.Broadcast(feed.SSEEvent{
			Type: "reconcile",
			Data: e,
		})
		// Also log
		log.Printf("[RECON] %s %s: %s", e.Effect, e.State, e.Message)
	})

	// Handle Ctrl+C
	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("Shutting down...")
		close(done)
		recon.Stop()
	}()

	// Start feed server
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("Feed server error: %v", err)
		}
	}()
	go srv.StartHeartbeat(5*time.Second, done)

	log.Printf("Prod mode — reconciling. Feed: http://localhost:%s/api/v1/stream", defaultPort)

	recon.Run()
	return nil
}

func reconcileStatus(root string) error {
	d, err := daemon.New(root)
	if err != nil {
		return err
	}
	if err := d.RunOnce(); err != nil {
		return err
	}

	recon := daemon.NewReconciler(root, d.GraphData())
	if err := recon.Scan(); err != nil {
		return err
	}

	fmt.Print(recon.Status())
	return nil
}

func simulate() error {
	// Parse: gotyped simulate --fail Effect [path]
	//        gotyped simulate --drift Effect.invariant [path]
	root := "."

	var failEffect, driftSpec string
	for i := 2; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "--fail" && i+1 < len(os.Args) {
			failEffect = os.Args[i+1]
			i++
		} else if a == "--drift" && i+1 < len(os.Args) {
			driftSpec = os.Args[i+1]
			i++
		} else if !strings.HasPrefix(a, "-") {
			root = a
		}
	}

	if failEffect == "" && driftSpec == "" {
		return fmt.Errorf("usage: gotyped simulate --fail Effect or --drift Effect.invariant")
	}

	d, err := daemon.New(root)
	if err != nil {
		return err
	}
	if err := d.RunOnce(); err != nil {
		return err
	}

	recon := daemon.NewReconciler(root, d.GraphData())
	if err := recon.Scan(); err != nil {
		return err
	}

	recon.OnEventFunc(func(e daemon.ReconcileEvent) {
		stateIcon := "✓"
		switch e.State {
		case "failed":
			stateIcon = "✗"
		case "drifted":
			stateIcon = "⚠"
		case "tainted":
			stateIcon = "↳"
		}
		fmt.Printf("%s [%s] %s: %s\n", stateIcon, e.Effect, e.State, e.Message)
		for _, a := range e.Actions {
			fmt.Printf("  → action: %s\n", a)
		}
	})

	if failEffect != "" {
		recon.SimulateFail(failEffect)
	}
	if driftSpec != "" {
		parts := strings.SplitN(driftSpec, ".", 2)
		if len(parts) != 2 {
			return fmt.Errorf("drift spec must be Effect.invariant, got: %s", driftSpec)
		}
		recon.SimulateDrift(parts[0], parts[1])
	}

	return nil
}

func status(root string) error {
	d, err := daemon.New(root)
	if err != nil {
		return err
	}
	if err := d.RunOnce(); err != nil {
		return err
	}
	fmt.Println(d.Status())
	return nil
}

func graph(root string) error {
	d, err := daemon.New(root)
	if err != nil {
		return err
	}
	if err := d.RunOnce(); err != nil {
		return err
	}
	fmt.Print(d.Graph())
	return nil
}

// connectFeed connects to the SSE stream and prints events to stdout.
func connectFeed(addr string) error {
	url := fmt.Sprintf("http://%s/api/v1/stream", addr)
	log.Printf("Connecting to %s ...", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("connecting to feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feed returned %d", resp.StatusCode)
	}

	log.Println("Connected. Streaming events...")

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			fmt.Println(line)
		}
	}

	return scanner.Err()
}
