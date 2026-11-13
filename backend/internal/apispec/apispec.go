// Package apispec checks handler responses against api/openapi.yaml, the
// API's source of truth (D-18, D-59). Handler tests call Check after each
// request; the route table test in cmd/server keeps the operations in step.
package apispec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

var (
	once     sync.Once
	loadErr  error
	compiler *jsonschema.Compiler
	paths    map[string]any
	root     any
	specURL  = "openapi.yaml"
)

// load reads the spec once per test binary. Its location is fixed relative
// to this file, so every package's tests find it.
func load() {
	_, here, _, _ := runtime.Caller(0)
	file := filepath.Join(filepath.Dir(here), "..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(file)
	if err != nil {
		loadErr = err
		return
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		loadErr = fmt.Errorf("parse %s: %w", file, err)
		return
	}
	// OpenAPI 3.0 says nullable: true where JSON Schema says type: [T,
	// "null"]; the spec is 3.0 for the Go client generator's sake.
	doc = unnullable(doc)
	// Round-trip through JSON: the validator wants JSON's types (float64
	// numbers, string keys), and yaml.v3 gives ints and map[string]any.
	js, err := json.Marshal(doc)
	if err != nil {
		loadErr = err
		return
	}
	jdoc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(js)))
	if err != nil {
		loadErr = err
		return
	}
	compiler = jsonschema.NewCompiler()
	if err := compiler.AddResource(specURL, jdoc); err != nil {
		loadErr = err
		return
	}
	root = jdoc
	paths, _ = jdoc.(map[string]any)["paths"].(map[string]any)
}

// Check fails the test when body is not what the spec says method path
// answers with status, or when the spec has no such response. body is the
// decoded JSON envelope (or nil for a response with no body).
func Check(t *testing.T, method, path string, status int, body any) {
	t.Helper()
	once.Do(load)
	if loadErr != nil {
		t.Fatalf("apispec: %v", loadErr)
	}
	template := match(path)
	if template == "" {
		t.Errorf("apispec: no spec path matches %s", path)
		return
	}
	item := paths[template].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	if op == nil {
		t.Errorf("apispec: %s %s is not in the spec", method, template)
		return
	}
	responses, _ := op["responses"].(map[string]any)
	code := strconv.Itoa(status)
	resp, ok := responses[code].(map[string]any)
	if !ok {
		if status >= 400 {
			resp, ok = responses["default"].(map[string]any)
		}
		if !ok {
			t.Errorf("apispec: %s %s has no %d response in the spec", method, template, status)
			return
		}
		code = "default"
	}
	pointer := "/paths/" + escape(template) + "/" + strings.ToLower(method) + "/responses/" + code
	if ref, isRef := resp["$ref"].(string); isRef {
		pointer = strings.TrimPrefix(ref, "#")
		resp = lookup(pointer)
	}
	content, _ := resp["content"].(map[string]any)
	if content == nil {
		if body != nil {
			t.Errorf("apispec: %s %s %d has no body in the spec, got %v", method, template, status, body)
		}
		return
	}
	schema, err := compiler.Compile(specURL + "#" + pointer + "/content/application~1json/schema")
	if err != nil {
		t.Fatalf("apispec: compile %s: %v", pointer, err)
	}
	if err := schema.Validate(body); err != nil {
		t.Errorf("apispec: %s %s %d body does not match the spec:\n%v", method, template, status, err)
	}
}

// unnullable rewrites every {nullable: true, …} into a schema JSON Schema
// understands: type: [T, "null"] for a typed schema, anyOf [the rest, null]
// for a $ref or allOf.
func unnullable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			x[k] = unnullable(child)
		}
		if n, ok := x["nullable"].(bool); ok && n {
			delete(x, "nullable")
			if t, ok := x["type"].(string); ok {
				x["type"] = []any{t, "null"}
				return x
			}
			rest := map[string]any{}
			for k, child := range x {
				rest[k] = child
			}
			return map[string]any{"anyOf": []any{rest, map[string]any{"type": "null"}}}
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = unnullable(child)
		}
		return x
	}
	return v
}

// lookup resolves a JSON pointer inside the document to a mapping.
func lookup(pointer string) map[string]any {
	var cur any = root
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	m, _ := cur.(map[string]any)
	return m
}

// match finds the spec path template for a concrete request path.
func match(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	segs := strings.Split(path, "/")
	best := ""
	for template := range paths {
		ts := strings.Split(template, "/")
		if len(ts) != len(segs) {
			continue
		}
		ok, literal := true, 0
		for i := range ts {
			switch {
			case strings.HasPrefix(ts[i], "{"):
			case ts[i] == segs[i]:
				literal++
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok && (best == "" || literal > strings.Count(best, "/")-strings.Count(best, "{")) {
			best = template
		}
	}
	return best
}

func escape(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "~", "~0"), "/", "~1")
}
