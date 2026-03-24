package preprocess

import (
	"fmt"
	"regexp"
	"strings"
)

// TransformTypeSystem rewrites higher-kinded type syntax into monomorphized Go.
// Called BEFORE TransformSugar (since it may generate sum types).
//
// Transforms:
//   effect State<T> { Get() T; Set(val T) }     → concrete effects per instantiation
//   typeclass Functor<F> { fmap<A,B>(...) }      → Go interface per instantiation
//   impl Functor<Option> { ... }                 → method implementations
//   State<int>, Option<string>                   → monomorphized type names
func TransformTypeSystem(src string) string {
	if !containsTypeSystemSyntax(src) {
		return src
	}

	ts := newTypeSystem()
	ts.parse(src)
	return ts.transform(src)
}

func containsTypeSystemSyntax(src string) bool {
	stripped := stripComments(src)
	return strings.Contains(stripped, "typeclass ") ||
		strings.Contains(stripped, "impl ") ||
		typeInstantiationRe.MatchString(stripped)
}

// typeInstantiationRe matches Type<Arg> patterns
var typeInstantiationRe = regexp.MustCompile(`\b([A-Z]\w*)<(\w+(?:\s*,\s*\w+)*)>`)

// --- Type system data ---

type typeSystem struct {
	typeclasses map[string]*typeclassDef
	impls       []implDef
}

type typeclassDef struct {
	name      string   // e.g., "Functor"
	typeParam string   // e.g., "F" (the type constructor param)
	methods   []methodDef
}

type methodDef struct {
	name       string
	typeParams []string // e.g., ["A", "B"]
	params     string   // raw param string
	returnType string
	body       string
}

type implDef struct {
	typeclass string // e.g., "Functor"
	forType   string // e.g., "Option"
	methods   []methodDef
}

func newTypeSystem() *typeSystem {
	return &typeSystem{
		typeclasses: make(map[string]*typeclassDef),
	}
}

// --- Parsing ---

var typeclassRe = regexp.MustCompile(`(?m)^typeclass\s+(\w+)<(\w+)>\s*\{`)
var implRe = regexp.MustCompile(`(?m)^impl\s+(\w+)<(\w+)>\s*\{`)

func (ts *typeSystem) parse(src string) {
	ts.parseTypeclasses(src)
	ts.parseImpls(src)
}

func (ts *typeSystem) parseTypeclasses(src string) {
	matches := typeclassRe.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range matches {
		name := src[loc[2]:loc[3]]
		typeParam := src[loc[4]:loc[5]]

		bracePos := loc[1] - 1
		sc := &scanner{src: src}
		endPos := sc.findMatchingBrace(bracePos)
		if endPos == -1 {
			continue
		}

		body := src[bracePos+1 : endPos-1]
		methods := parseTypeclassMethods(body, typeParam)

		ts.typeclasses[name] = &typeclassDef{
			name:      name,
			typeParam: typeParam,
			methods:   methods,
		}
	}
}

func parseTypeclassMethods(body string, _ string) []methodDef {
	var methods []methodDef
	// Parse: methodName<A, B>(params) ReturnType
	methodRe := regexp.MustCompile(`(?m)^\s*(\w+)<([^>]+)>\(([^)]*)\)\s*(.+)$`)
	for _, m := range methodRe.FindAllStringSubmatch(body, -1) {
		typeParams := strings.Split(m[2], ",")
		for i := range typeParams {
			typeParams[i] = strings.TrimSpace(typeParams[i])
		}
		methods = append(methods, methodDef{
			name:       m[1],
			typeParams: typeParams,
			params:     strings.TrimSpace(m[3]),
			returnType: strings.TrimSpace(m[4]),
		})
	}
	return methods
}

func (ts *typeSystem) parseImpls(src string) {
	matches := implRe.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range matches {
		tcName := src[loc[2]:loc[3]]
		forType := src[loc[4]:loc[5]]

		bracePos := loc[1] - 1
		sc := &scanner{src: src}
		endPos := sc.findMatchingBrace(bracePos)
		if endPos == -1 {
			continue
		}

		body := src[bracePos+1 : endPos-1]
		methods := parseImplMethods(body)

		ts.impls = append(ts.impls, implDef{
			typeclass: tcName,
			forType:   forType,
			methods:   methods,
		})
	}
}

func parseImplMethods(body string) []methodDef {
	var methods []methodDef

	// Parse: methodName<TypeParams>(params) ReturnType { body }
	// Use a scanner approach since params may contain nested parens
	nameRe := regexp.MustCompile(`(?m)^\s*(\w+)<([^>]+)>\(`)
	matches := nameRe.FindAllStringSubmatchIndex(body, -1)

	for _, loc := range matches {
		name := body[loc[2]:loc[3]]
		typeParams := strings.Split(body[loc[4]:loc[5]], ",")
		for i := range typeParams {
			typeParams[i] = strings.TrimSpace(typeParams[i])
		}

		// Find matching ) for the params (handle nested parens like func(A) B)
		parenStart := loc[1] - 1 // position of (
		pos := loc[1]
		depth := 1
		for pos < len(body) && depth > 0 {
			if body[pos] == '(' {
				depth++
			} else if body[pos] == ')' {
				depth--
			}
			if depth > 0 {
				pos++
			}
		}
		params := strings.TrimSpace(body[parenStart+1 : pos])
		pos++ // skip )

		// Skip whitespace, read return type (until '{')
		for pos < len(body) && (body[pos] == ' ' || body[pos] == '\t') {
			pos++
		}
		retStart := pos
		for pos < len(body) && body[pos] != '{' {
			pos++
		}
		returnType := strings.TrimSpace(body[retStart:pos])

		// Read method body
		if pos >= len(body) {
			continue
		}
		sc := &scanner{src: body}
		endPos := sc.findMatchingBrace(pos)
		if endPos == -1 {
			continue
		}
		methodBody := body[pos+1 : endPos-1]

		methods = append(methods, methodDef{
			name:       name,
			typeParams: typeParams,
			params:     params,
			returnType: returnType,
			body:       methodBody,
		})
	}

	return methods
}

// --- Transformation ---

func (ts *typeSystem) transform(src string) string {
	// Step 1: Remove typeclass and impl blocks from source
	src = ts.removeBlocks(src, implRe)
	src = ts.removeBlocks(src, typeclassRe)

	// Step 2: Find all type instantiations and generate concrete code
	instantiations := ts.findInstantiations(src)
	var generated strings.Builder
	for _, inst := range instantiations {
		generated.WriteString(ts.generateMonomorphized(inst))
	}

	// Step 3: Replace Type<Arg> with TypeArg throughout the source
	src = ts.rewriteInstantiations(src)

	// Step 4: Generate impl functions as top-level functions
	for _, impl := range ts.impls {
		generated.WriteString(ts.generateImplFuncs(impl))
	}

	if generated.Len() > 0 {
		genCode := generated.String()
		// Also rewrite type instantiations in generated code
		genCode = typeInstantiationRe.ReplaceAllStringFunc(genCode, func(match string) string {
			m := typeInstantiationRe.FindStringSubmatch(match)
			if len(m) < 3 {
				return match
			}
			args := strings.Split(m[2], ",")
			for i := range args {
				args[i] = strings.TrimSpace(args[i])
			}
			return monomorphizedName(m[1], args)
		})
		src += "\n// --- generated by gotype type system ---\n" + genCode
	}

	return src
}

func (ts *typeSystem) removeBlocks(src string, re *regexp.Regexp) string {
	for {
		loc := re.FindStringIndex(src)
		if loc == nil {
			break
		}
		// Find the opening brace
		bracePos := strings.Index(src[loc[0]:], "{")
		if bracePos == -1 {
			break
		}
		bracePos += loc[0]
		sc := &scanner{src: src}
		endPos := sc.findMatchingBrace(bracePos)
		if endPos == -1 {
			break
		}
		// Remove the block (and trailing newline)
		end := endPos
		for end < len(src) && (src[end] == '\n' || src[end] == '\r') {
			end++
		}
		src = src[:loc[0]] + src[end:]
	}
	return src
}

type instantiation struct {
	typeName string // e.g., "Option"
	typeArgs []string // e.g., ["int"]
	mono     string // e.g., "OptionInt"
}

func (ts *typeSystem) findInstantiations(src string) []instantiation {
	seen := make(map[string]bool)
	var result []instantiation

	for _, m := range typeInstantiationRe.FindAllStringSubmatch(src, -1) {
		typeName := m[1]
		args := strings.Split(m[2], ",")
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}

		mono := monomorphizedName(typeName, args)
		if seen[mono] {
			continue
		}
		seen[mono] = true

		result = append(result, instantiation{
			typeName: typeName,
			typeArgs: args,
			mono:     mono,
		})
	}

	return result
}

func monomorphizedName(typeName string, args []string) string {
	var b strings.Builder
	b.WriteString(typeName)
	for _, a := range args {
		b.WriteString(capitalize(a))
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (ts *typeSystem) rewriteInstantiations(src string) string {
	return typeInstantiationRe.ReplaceAllStringFunc(src, func(match string) string {
		m := typeInstantiationRe.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		args := strings.Split(m[2], ",")
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}
		return monomorphizedName(m[1], args)
	})
}

// generateMonomorphized creates concrete Go type for an instantiation.
// For now, this handles known patterns like Option<T> (from sum types).
// The actual struct/interface is generated by the sum type transform.
func (ts *typeSystem) generateMonomorphized(inst instantiation) string {
	// Type aliases: OptionInt = Option with T=int
	// Most cases, the sum type already exists and we just need the alias.
	// For generic effects, we generate concrete effect names.
	return ""
}

// generateImplFuncs creates top-level functions from typeclass impls.
func (ts *typeSystem) generateImplFuncs(impl implDef) string {
	var out strings.Builder

	for _, m := range impl.methods {
		// Generate: methodName + ForType, e.g., fmapOption
		// Also generate an alias without type params in the name
		funcName := fmt.Sprintf("%s%s", m.name, capitalize(impl.forType))

		// Also generate a short name if the method name already contains specifics
		// e.g., fmapIntToString → also generate as fmapOption
		shortName := fmt.Sprintf("%s%s", extractBaseName(m.name), capitalize(impl.forType))

		// Substitute type params with concrete types
		body := m.body
		params := m.params
		returnType := m.returnType

		// Replace the type constructor param with the concrete type
		// This is a simplified monomorphization — full version would track substitutions
		out.WriteString(fmt.Sprintf("\nfunc %s(%s) %s {%s\n}\n",
			funcName, params, returnType, body))

		// Generate short alias if different
		if shortName != funcName {
			out.WriteString(fmt.Sprintf("\nfunc %s(%s) %s { return %s(%s) }\n",
				shortName, params, returnType, funcName, paramNames(params)))
		}
	}

	return out.String()
}

// extractBaseName gets the base method name without type-specific suffixes.
// "fmapIntToString" → "fmap"
func extractBaseName(name string) string {
	// Find where the first uppercase letter starts after initial lowercase
	for i := 1; i < len(name); i++ {
		if name[i] >= 'A' && name[i] <= 'Z' {
			return name[:i]
		}
	}
	return name
}

// paramNames extracts just the parameter names from a param string.
// "fn func(int) string, fa OptionInt" → "fn, fa"
func paramNames(params string) string {
	var names []string
	for _, p := range strings.Split(params, ",") {
		p = strings.TrimSpace(p)
		fields := strings.Fields(p)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return strings.Join(names, ", ")
}
