package preprocess

import (
	"strings"
	"testing"
)

func TestTypeclassParsing(t *testing.T) {
	src := `package main

typeclass Functor<F> {
	fmap<A, B>(fn func(A) B, fa F<A>) F<B>
}

func main() {}
`
	result := TransformTypeSystem(src)
	// Typeclass block should be removed
	if strings.Contains(result, "typeclass") {
		t.Errorf("typeclass block should be removed.\ngot:\n%s", result)
	}
}

func TestImplParsing(t *testing.T) {
	src := `package main

typeclass Functor<F> {
	fmap<A, B>(fn func(A) B, fa F<A>) F<B>
}

impl Functor<Option> {
	fmap<A, B>(fn func(A) B, fa OptionA) OptionB {
		return OptionB{}
	}
}

func main() {}
`
	result := TransformTypeSystem(src)
	// Should generate a concrete function
	if !strings.Contains(result, "func fmapOption") {
		t.Errorf("impl function not generated.\ngot:\n%s", result)
	}
	// typeclass and impl blocks removed
	if strings.Contains(result, "typeclass") || strings.Contains(result, "impl Functor") {
		t.Errorf("blocks not removed.\ngot:\n%s", result)
	}
}

func TestTypeInstantiationRewrite(t *testing.T) {
	src := `package main

var x Option<int>
var y Result<string, error>

func main() {}
`
	result := TransformTypeSystem(src)
	if !strings.Contains(result, "OptionInt") {
		t.Errorf("Option<int> not rewritten.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "ResultStringError") {
		t.Errorf("Result<string, error> not rewritten.\ngot:\n%s", result)
	}
}

func TestNoTypeSystemPassthrough(t *testing.T) {
	src := `package main

func main() {
	fmt.Println("hello")
}
`
	result := TransformTypeSystem(src)
	if result != src {
		t.Errorf("non-typesystem source should be unchanged.\ngot:\n%s", result)
	}
}

func TestEffectCheckUndeclared(t *testing.T) {
	src := `package main

func main() {
	name := perform AskName
}
`
	errors := CheckEffects(src)
	found := false
	for _, e := range errors {
		if strings.Contains(e, "undeclared effect") && strings.Contains(e, "AskName") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected undeclared effect error. got: %v", errors)
	}
}

func TestEffectCheckUnhandled(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName
}

func main() {
	getName()
}
`
	errors := CheckEffects(src)
	found := false
	for _, e := range errors {
		if strings.Contains(e, "unhandled effect") && strings.Contains(e, "AskName") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unhandled effect error. got: %v", errors)
	}
}

func TestEffectCheckPasses(t *testing.T) {
	src := `package main

effect AskName(string)

func getName() string {
	return perform AskName
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
	errors := CheckEffects(src)
	if len(errors) != 0 {
		t.Errorf("expected no errors. got: %v", errors)
	}
}
