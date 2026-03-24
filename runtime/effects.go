// Package runtime provides the algebraic effects runtime for generated Go code.
//
// Effects are single named operations:
//
//	effect AskName(string)     // declares effect returning string
//	name := perform AskName    // raises effect, blocks until resumed
//
//	handle {
//	    doWork()
//	} with {
//	case AskName:
//	    resume("Alice")
//	}
//
// Under the hood: the effectful code runs in a goroutine, perform sends
// on a channel and blocks, the handler event loop dispatches and resume
// sends the value back.
package runtime

import (
	"fmt"
	goruntime "runtime"
	"sync"
)

// HandlerFunc is called when an effect is performed.
// args contains the arguments passed to perform.
// resume continues execution at the perform site with the given value.
// If resume is not called, the effectful computation is abandoned.
type HandlerFunc func(args []any, resume func(any))

// Handlers maps effect names to handler functions.
type Handlers map[string]HandlerFunc

// resumeResult carries the value back to the perform site, or signals abort.
type resumeResult struct {
	value any
	abort bool
}

// performReq is sent from the performing goroutine to the handler.
type performReq struct {
	effect   string
	args     []any
	resumeCh chan resumeResult
}

// handlerFrame is a single handler on the stack.
type handlerFrame struct {
	effect  string
	handler HandlerFunc
}

// Context holds the handler stack and perform channel for an effectful computation.
type Context struct {
	mu        sync.Mutex
	stack     []handlerFrame
	performCh chan performReq
}

// NewContext creates a new effect context.
func NewContext() *Context {
	return &Context{
		performCh: make(chan performReq),
	}
}

// Handle pushes a handler for the given effect onto the stack.
func (c *Context) Handle(effect string, h HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stack = append(c.stack, handlerFrame{effect: effect, handler: h})
}

// Unhandle removes the topmost handler for the given effect.
func (c *Context) Unhandle(effect string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.stack) - 1; i >= 0; i-- {
		if c.stack[i].effect == effect {
			c.stack = append(c.stack[:i], c.stack[i+1:]...)
			return
		}
	}
}

// findHandler finds the handler for the given effect.
func (c *Context) findHandler(effect string) (HandlerFunc, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.stack) - 1; i >= 0; i-- {
		if c.stack[i].effect == effect {
			return c.stack[i].handler, true
		}
	}
	return nil, false
}

// Perform raises an effect. It sends a request to the handler
// and blocks until the handler calls resume.
// If the handler does not resume, the goroutine is cleanly terminated.
func Perform(ctx *Context, effect string, args ...any) any {
	req := performReq{
		effect:   effect,
		args:     args,
		resumeCh: make(chan resumeResult, 1),
	}
	ctx.performCh <- req
	result := <-req.resumeCh
	if result.abort {
		goruntime.Goexit()
	}
	return result.value
}

// Run executes an effectful function, dispatching performs to handlers.
func Run(ctx *Context, fn func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		fn()
	}()

	for {
		select {
		case req := <-ctx.performCh:
			handler, ok := ctx.findHandler(req.effect)
			if !ok {
				panic(fmt.Sprintf("unhandled effect: %s", req.effect))
			}
			resumed := false
			handler(req.args, func(val any) {
				resumed = true
				req.resumeCh <- resumeResult{value: val}
			})
			if !resumed {
				req.resumeCh <- resumeResult{abort: true}
				<-done
				return
			}
		case <-done:
			return
		}
	}
}

// Try runs fn with the given handlers, then removes them.
// This is the core construct for handle/with blocks.
func Try(ctx *Context, h Handlers, fn func()) {
	effects := make([]string, 0, len(h))
	for effect, handler := range h {
		ctx.Handle(effect, handler)
		effects = append(effects, effect)
	}
	defer func() {
		for i := len(effects) - 1; i >= 0; i-- {
			ctx.Unhandle(effects[i])
		}
	}()
	Run(ctx, fn)
}
