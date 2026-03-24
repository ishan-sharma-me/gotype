package runtime

import (
	"strings"
	"testing"
)

func TestBasicPerformResume(t *testing.T) {
	ctx := NewContext()
	var result string

	Try(ctx, Handlers{
		"AskName": func(args []any, resume func(any)) {
			resume("Alice")
		},
	}, func() {
		result = Perform(ctx, "AskName").(string)
	})

	if result != "Alice" {
		t.Errorf("result = %q, want %q", result, "Alice")
	}
}

func TestPerformWithArgs(t *testing.T) {
	ctx := NewContext()
	var result string

	Try(ctx, Handlers{
		"Greet": func(args []any, resume func(any)) {
			name := args[0].(string)
			resume("Hello, " + name + "!")
		},
	}, func() {
		result = Perform(ctx, "Greet", "World").(string)
	})

	if result != "Hello, World!" {
		t.Errorf("result = %q, want %q", result, "Hello, World!")
	}
}

func TestMultiplePerforms(t *testing.T) {
	ctx := NewContext()
	var log []string

	Try(ctx, Handlers{
		"Log": func(args []any, resume func(any)) {
			log = append(log, args[0].(string))
			resume(nil)
		},
	}, func() {
		Perform(ctx, "Log", "one")
		Perform(ctx, "Log", "two")
		Perform(ctx, "Log", "three")
	})

	if len(log) != 3 || log[0] != "one" || log[1] != "two" || log[2] != "three" {
		t.Errorf("log = %v", log)
	}
}

func TestNestedHandlers(t *testing.T) {
	ctx := NewContext()
	var result string

	Try(ctx, Handlers{
		"Outer": func(args []any, resume func(any)) {
			resume("outer-value")
		},
	}, func() {
		Try(ctx, Handlers{
			"Inner": func(args []any, resume func(any)) {
				resume("inner-value")
			},
		}, func() {
			a := Perform(ctx, "Outer").(string)
			b := Perform(ctx, "Inner").(string)
			result = a + "+" + b
		})
	})

	if result != "outer-value+inner-value" {
		t.Errorf("result = %q, want %q", result, "outer-value+inner-value")
	}
}

func TestHandlerOverride(t *testing.T) {
	ctx := NewContext()
	var result string

	Try(ctx, Handlers{
		"AskName": func(args []any, resume func(any)) {
			resume("Outer")
		},
	}, func() {
		Try(ctx, Handlers{
			"AskName": func(args []any, resume func(any)) {
				resume("Inner")
			},
		}, func() {
			result = Perform(ctx, "AskName").(string)
		})
	})

	if result != "Inner" {
		t.Errorf("result = %q, want %q", result, "Inner")
	}
}

func TestNoResumeAbandons(t *testing.T) {
	ctx := NewContext()
	reached := false

	Try(ctx, Handlers{
		"Abort": func(args []any, resume func(any)) {
			// Don't call resume — abandon the computation
		},
	}, func() {
		Perform(ctx, "Abort")
		reached = true
	})

	if reached {
		t.Error("code after non-resuming perform should not execute")
	}
}

func TestEffectBubblesToCaller(t *testing.T) {
	ctx := NewContext()

	getName := func() string {
		return Perform(ctx, "AskName").(string)
	}

	makeFriends := func() string {
		a := getName()
		b := getName()
		return a + " & " + b
	}

	var result string
	callCount := 0

	Try(ctx, Handlers{
		"AskName": func(args []any, resume func(any)) {
			callCount++
			if callCount == 1 {
				resume("Arya")
			} else {
				resume("Gendry")
			}
		},
	}, func() {
		result = makeFriends()
	})

	if result != "Arya & Gendry" {
		t.Errorf("result = %q, want %q", result, "Arya & Gendry")
	}
}

func TestMultipleEffectsInOneHandle(t *testing.T) {
	ctx := NewContext()
	var log []string

	Try(ctx, Handlers{
		"AskName": func(args []any, resume func(any)) {
			resume("Alice")
		},
		"Log": func(args []any, resume func(any)) {
			log = append(log, args[0].(string))
			resume(nil)
		},
	}, func() {
		name := Perform(ctx, "AskName").(string)
		Perform(ctx, "Log", "Hello, "+name)
	})

	if len(log) != 1 || log[0] != "Hello, Alice" {
		t.Errorf("log = %v, want [Hello, Alice]", log)
	}
}

func TestUnhandledEffectPanics(t *testing.T) {
	ctx := NewContext()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for unhandled effect")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "unhandled effect") {
			t.Errorf("unexpected panic: %v", r)
		}
	}()

	Run(ctx, func() {
		Perform(ctx, "Missing")
	})
}

func TestVoidPerform(t *testing.T) {
	ctx := NewContext()
	executed := false

	Try(ctx, Handlers{
		"Log": func(args []any, resume func(any)) {
			resume(nil)
		},
	}, func() {
		Perform(ctx, "Log", "message")
		executed = true
	})

	if !executed {
		t.Error("code after void perform should execute")
	}
}
