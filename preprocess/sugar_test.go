package preprocess

import (
	"strings"
	"testing"
)

func TestPipelineSimple(t *testing.T) {
	src := `package main

func main() {
	result := data |> transform |> validate
}
`
	result := TransformSugar(src)
	if !strings.Contains(result, "validate(transform(data))") {
		t.Errorf("pipeline not rewritten.\ngot:\n%s", result)
	}
}

func TestPipelineWithArgs(t *testing.T) {
	src := `package main

func main() {
	result := data |> transform |> save("db")
}
`
	result := TransformSugar(src)
	if !strings.Contains(result, `save(transform(data), "db")`) {
		t.Errorf("pipeline with args not rewritten.\ngot:\n%s", result)
	}
}

func TestPipelineNoChange(t *testing.T) {
	src := `package main

func main() {
	x := f(a)
}
`
	result := TransformSugar(src)
	if result != src {
		t.Errorf("non-pipeline source should be unchanged.\ngot:\n%s", result)
	}
}

func TestSumTypeBasic(t *testing.T) {
	src := `package main

type Result = Success{value string} | Failure{err error}
`
	result := TransformSugar(src)
	if !strings.Contains(result, "type Result interface") {
		t.Errorf("sum type interface not generated.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "type Success struct") {
		t.Errorf("Success struct not generated.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "type Failure struct") {
		t.Errorf("Failure struct not generated.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "__isResult()") {
		t.Errorf("seal method not generated.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "value string") {
		t.Errorf("field name not in struct.\ngot:\n%s", result)
	}
}

func TestSumTypeNoFields(t *testing.T) {
	src := `package main

type Color = Red | Green | Blue
`
	result := TransformSugar(src)
	if !strings.Contains(result, "type Color interface") {
		t.Errorf("interface not generated.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "type Red struct{}") {
		t.Errorf("empty variant struct not generated.\ngot:\n%s", result)
	}
}

func TestMatchBasic(t *testing.T) {
	src := `package main

func handle(r Result) {
	match r {
	case Success{v}:
		fmt.Println(v)
	case Failure{e}:
		log.Fatal(e)
	}
}
`
	result := TransformSugar(src)
	if !strings.Contains(result, "switch __m := (r).(type)") {
		t.Errorf("match not converted to type switch.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "case Success:") {
		t.Errorf("Success case not in switch.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "v := __m.v") {
		t.Errorf("field binding not generated.\ngot:\n%s", result)
	}
}

func TestMatchNoBindings(t *testing.T) {
	src := `package main

func check(c Color) string {
	match c {
	case Red:
		return "red"
	case Green:
		return "green"
	case Blue:
		return "blue"
	}
}
`
	result := TransformSugar(src)
	if !strings.Contains(result, "case Red:") {
		t.Errorf("Red case missing.\ngot:\n%s", result)
	}
	if !strings.Contains(result, "case Blue:") {
		t.Errorf("Blue case missing.\ngot:\n%s", result)
	}
}

func TestNoSugarPassthrough(t *testing.T) {
	src := `package main

func main() {
	fmt.Println("hello")
}
`
	result := TransformSugar(src)
	if result != src {
		t.Errorf("non-sugar source should be unchanged.\ngot:\n%s", result)
	}
}
