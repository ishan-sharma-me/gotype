package preprocess

import (
	"strings"
	"testing"
)

func TestTransformEffects_NoEffects(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`
	result := TransformEffects(src)
	if result != src {
		t.Errorf("non-effect source should be unchanged.\ngot:\n%s", result)
	}
}

func TestTransformEffectDeclaration(t *testing.T) {
	src := `package main

effect AskName(string)
effect Log(msg string)

func main() {}
`
	result := TransformEffects(src)
	if strings.Contains(result, "effect AskName") {
		t.Error("effect declaration should be removed")
	}
	if !strings.Contains(result, "__gotype_effect:AskName") {
		t.Error("AskName should be preserved as comment")
	}
}

func TestInjectsEffReqType(t *testing.T) {
	src := `package main

effect AskName(string)

func main() {}
`
	result := TransformEffects(src)
	if !strings.Contains(result, "type __effReq struct") {
		t.Errorf("__effReq type not injected.\ngot:\n%s", result)
	}
}

func TestNoRuntimeImport(t *testing.T) {
	src := `package main

import "fmt"

effect AskName(string)

func main() {
	fmt.Println("hi")
}
`
	result := TransformEffects(src)
	if strings.Contains(result, "gotype/runtime") {
		t.Error("should NOT import gotype/runtime")
	}
}

func TestPerformNoArgs(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName
}

func main() {}
`
	result := TransformEffects(src)
	if !strings.Contains(result, `__eff <- __effReq{"AskName"`) {
		t.Errorf("perform not transformed.\ngot:\n%s", result)
	}
}

func TestPerformWithArgs(t *testing.T) {
	src := `package main

effect Greet(name string) string

func greet() string {
	return perform Greet("World")
}

func main() {}
`
	result := TransformEffects(src)
	if !strings.Contains(result, `[]any{"World"}`) {
		t.Errorf("perform args not wrapped.\ngot:\n%s", result)
	}
}

func TestHandleWith(t *testing.T) {
	src := `package main

effect AskName(string)

func main() {
	handle {
		fmt.Println("hello")
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	result := TransformEffects(src)
	if !strings.Contains(result, `__eff := make(chan __effReq)`) {
		t.Errorf("handle not creating channel.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `go func()`) {
		t.Errorf("handle not spawning goroutine.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `case "AskName"`) {
		t.Errorf("case clause missing.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `__req.rch <- "Alice"`) {
		t.Errorf("resume not transformed.\ngot:\n%s", result)
	}
}

func TestHandleWithParams(t *testing.T) {
	src := `package main

effect Log(msg string)

func doLog() {
	perform Log("hello")
}

func main() {
	handle {
		doLog()
	} with {
	case Log(msg string):
		fmt.Println(msg)
		resume()
	}
}
`
	result := TransformEffects(src)
	if !strings.Contains(result, `msg := __req.args[0].(string)`) {
		t.Errorf("param unpacking missing.\ngot:\n%s", result)
	}
}

func TestAutoInjectEffParam(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName.(string)
}

func main() {
	handle {
		getName()
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	result := TransformEffects(src)
	// getName should get __eff parameter injected
	if !strings.Contains(result, "func getName(__eff chan<- __effReq)") {
		t.Errorf("__eff param not auto-injected into getName.\ngot:\n%s", result)
	}
	// Call site inside handle should get __eff argument
	if !strings.Contains(result, "getName(__eff)") {
		t.Errorf("__eff arg not injected at call site.\ngot:\n%s", result)
	}
}

func TestAutoInjectTransitive(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName.(string)
}

func makeFriends() {
	getName()
}

func main() {
	handle {
		makeFriends()
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	result := TransformEffects(src)
	// Both functions should get __eff injected
	if !strings.Contains(result, "func getName(__eff chan<- __effReq)") {
		t.Errorf("getName missing __eff param.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "func makeFriends(__eff chan<- __effReq)") {
		t.Errorf("makeFriends missing __eff param (transitive).\ngot:\n%s", result)
	}
	// Call from makeFriends to getName should have __eff
	if !strings.Contains(result, "getName(__eff)") {
		t.Errorf("getName call missing __eff arg.\ngot:\n%s", result)
	}
}

func TestMainNotInjected(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName.(string)
}

func main() {
	handle {
		getName()
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	result := TransformEffects(src)
	// main should NOT get __eff parameter — it uses handle/with
	if strings.Contains(result, "func main(__eff") {
		t.Errorf("main should not get __eff param.\ngot:\n%s", result)
	}
}

func TestSelfContainedOutput(t *testing.T) {
	src := `package main

import "fmt"

effect AskName(string)

func getName() string {
	return perform AskName.(string)
}

func main() {
	handle {
		fmt.Println(getName())
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	result := TransformEffects(src)
	if strings.Contains(result, "github.com/abstractnet") {
		t.Errorf("output must be self-contained Go.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "type __effReq struct") {
		t.Errorf("__effReq type missing.\ngot:\n%s", result)
	}
}
