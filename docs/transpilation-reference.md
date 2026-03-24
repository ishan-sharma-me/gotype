# Transpilation Reference

Exact input → output pairs showing what the gotype transpiler generates.

## Effect Declaration

**Input (.tg):**
```go
effect AskName(string)
```

**Output (.go):**
```go
// __gotype_effect:AskName
```

Effect declarations are stripped — they're metadata, not runtime code.

---

## Perform

**Input (.tg):**
```go
name := perform AskName.(string)
```

**Output (.go):**
```go
name := func() any { __rch := make(chan any, 1); __eff <- __effReq{"AskName", nil, __rch}; return <-__rch }().(string)
```

With arguments:

**Input (.tg):**
```go
perform Log("hello world")
```

**Output (.go):**
```go
func() any { __rch := make(chan any, 1); __eff <- __effReq{"Log", []any{"hello world"}, __rch}; return <-__rch }()
```

---

## Handle / With

**Input (.tg):**
```go
handle {
    name := getName()
    fmt.Println(name)
} with {
case AskName:
    resume("Alice")
case Log(msg string):
    fmt.Println(msg)
    resume()
}
```

**Output (.go):**
```go
func() {
    __eff := make(chan __effReq)
    __done := make(chan struct{})
    go func() {
        defer close(__done)
        name := getName(__eff)
        fmt.Println(name)
    }()
    for {
        select {
        case __req := <-__eff:
            switch __req.name {
            case "AskName":
                __req.rch <- "Alice"
            case "Log":
                msg := __req.args[0].(string)
                fmt.Println(msg)
                __req.rch <- nil
            }
        case <-__done:
            return
        }
    }
}()
```

---

## Automatic Effect Parameter Injection

Functions that contain `perform` (directly or transitively) automatically get
`__eff chan<- __effReq` injected as their first parameter. Call sites are updated too.

**Input (.tg):**
```go
func getName() string {
    return perform AskName.(string)
}
func greet() {
    fmt.Println(getName())
}
```

**Output (.go):**
```go
func getName(__eff chan<- __effReq) string {
    return func() any { __rch := make(chan any, 1); __eff <- __effReq{"AskName", nil, __rch}; return <-__rch }().(string)
}
func greet(__eff chan<- __effReq) {
    fmt.Println(getName(__eff))
}
```

---

## Generated Type

When a file uses effects, this type is injected once at the top:

```go
type __effReq struct{ name string; args []any; rch chan any }
```

No external imports. The output is self-contained Go.

---

## Test Blocks

**Input (.tg):**
```go
test "addition works" {
    result := 1 + 2
    assert result == 3
}
```

**Output (.go):**
```go
func TestAdditionWorks(t *testing.T) {
    result := 1 + 2
    if !(result == 3) { t.Fatalf("assert failed: result == 3") }
}
```

---

## Sum Types

**Input (.tg):**
```go
type Result = Success{value string} | Failure{err string}
```

**Output (.go):**
```go
type Result interface { __isResult() }
type Success struct { value string }
func (Success) __isResult() {}
type Failure struct { err string }
func (Failure) __isResult() {}
```

---

## Pattern Matching

**Input (.tg):**
```go
match r {
case Success{value}:
    fmt.Println(value)
case Failure{err}:
    log.Fatal(err)
}
```

**Output (.go):**
```go
switch __m := (r).(type) {
case Success:
    value := __m.value
    fmt.Println(value)
case Failure:
    err := __m.err
    log.Fatal(err)
}
```

---

## Pipeline

**Input (.tg):**
```go
result := data |> transform |> save("db")
```

**Output (.go):**
```go
result := save(transform(data), "db")
```

---

## Parallel

**Input (.tg):**
```go
parallel {
    branch "a" { doA() }
    branch "b" { doB() }
}
```

**Output (.go):**
```go
func() {
    var __wg sync.WaitGroup
    __wg.Add(2)
    go func() { defer __wg.Done(); doA() }()
    go func() { defer __wg.Done(); doB() }()
    __wg.Wait()
}()
```

---

## Race

**Input (.tg):**
```go
winner := race {
    branch "fast" { return "fast" }
    branch "slow" { return "slow" }
}
```

**Output (.go):**
```go
winner := func() any {
    __ctx, __cancel := context.WithCancel(context.Background())
    defer __cancel()
    __ch := make(chan any, 2)
    go func() { _ = __ctx; __ch <- "fast"; return }()
    go func() { _ = __ctx; __ch <- "slow"; return }()
    return <-__ch
}()
```

---

## Timeout

**Input (.tg):**
```go
timeout 5s {
    slowOp()
} on_timeout {
    fmt.Println("timed out")
}
```

**Output (.go):**
```go
func() {
    __ctx, __cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer __cancel()
    __done := make(chan struct{})
    go func() {
        defer close(__done)
        _ = __ctx
        slowOp()
    }()
    select {
    case <-__done:
    case <-__ctx.Done():
        fmt.Println("timed out")
    }
}()
```

---

## Type Instantiation

**Input (.tg):**
```go
var x Option<int>
```

**Output (.go):**
```go
var x OptionInt
```

All `Type<Arg>` patterns are monomorphized to `TypeArg`.
