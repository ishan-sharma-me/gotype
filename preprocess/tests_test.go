package preprocess

import (
	"strings"
	"testing"
)

func TestTransformTestBlock(t *testing.T) {
	src := `package main_test

test "addition works" {
	result := 1 + 2
	assert result == 3
}
`
	result := TransformTests(src)

	if !strings.Contains(result, "func TestAdditionWorks(t *testing.T)") {
		t.Errorf("test block not transformed to Go test func.\ngot:\n%s", result)
	}
	if strings.Contains(result, `test "addition`) {
		t.Error("test keyword should be removed from output")
	}
}

func TestTransformAssert(t *testing.T) {
	src := `package main_test

test "basic assert" {
	x := 42
	assert x == 42
	assert x > 0
}
`
	result := TransformTests(src)

	if !strings.Contains(result, `if !(x == 42)`) {
		t.Errorf("assert not transformed.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `t.Fatalf("assert failed:`) {
		t.Errorf("assert not generating t.Fatalf.\ngot:\n%s", result)
	}
}

func TestTransformTestWithHandleWith(t *testing.T) {
	// This tests the full pipeline: effects + test blocks
	src := `package main_test

effect AskName(string)

func getName() string {
	return perform AskName.(string)
}

test "getName returns prompted name" {
	handle {
		result := getName()
		assert result == "Alice"
	} with {
	case AskName:
		resume("Alice")
	}
}
`
	// TransformEffects first, then TransformTests
	result := TransformEffects(src)
	result = TransformTests(result)

	if !strings.Contains(result, "func TestGetNameReturnsPromptedName(t *testing.T)") {
		t.Errorf("test block not transformed.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `__eff := make(chan __effReq)`) {
		t.Errorf("handle/with inside test not transformed.\ngot:\n%s", result)
	}
	if !strings.Contains(result, `result == "Alice"`) {
		t.Errorf("assert expression missing.\ngot:\n%s", result)
	}
}

func TestTransformMultipleTests(t *testing.T) {
	src := `package main_test

test "first test" {
	assert 1 == 1
}

test "second test" {
	assert 2 == 2
}
`
	result := TransformTests(src)

	if !strings.Contains(result, "func TestFirstTest(t *testing.T)") {
		t.Errorf("first test not transformed.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "func TestSecondTest(t *testing.T)") {
		t.Errorf("second test not transformed.\ngot:\n%s", result)
	}
}

func TestTestNameToFuncName(t *testing.T) {
	tests := []struct {
		desc string
		want string
	}{
		{"addition works", "TestAdditionWorks"},
		{"getName returns prompted name", "TestGetNameReturnsPromptedName"},
		{"it handles edge-cases", "TestItHandlesEdgeCases"},
		{"a", "TestA"},
		{"123 numbers first", "Test123NumbersFirst"},
	}
	for _, tt := range tests {
		got := testNameToFuncName(tt.desc)
		if got != tt.want {
			t.Errorf("testNameToFuncName(%q) = %q, want %q", tt.desc, got, tt.want)
		}
	}
}

func TestNoTestSyntaxPassthrough(t *testing.T) {
	src := `package main

func main() {
	fmt.Println("no tests here")
}
`
	result := TransformTests(src)
	if result != src {
		t.Errorf("non-test source should be unchanged.\ngot:\n%s", result)
	}
}

func TestTestingImportAdded(t *testing.T) {
	src := `package main_test

import "fmt"

test "basic" {
	fmt.Println("hello")
	assert true
}
`
	result := TransformTests(src)
	if !strings.Contains(result, `"testing"`) {
		t.Errorf("testing import not added.\ngot:\n%s", result)
	}
}
