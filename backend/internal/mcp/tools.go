// Package mcp is Toft's MCP server (D-17, D-100): tools that follow tasks,
// declared in the OpenAPI spec (x-toft-tools, D-18), served over Streamable
// HTTP at /mcp to agents holding an OAuth access token. A tool runs its
// operations through the API's own router in-process, with the agent's
// token, so authentication, scopes, the agent policy and validation are the
// API's.
package mcp

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operation is one API operation as the spec describes it.
type Operation struct {
	ID     string
	Method string
	Path   string // "/api/v1/services/{serviceID}"
	// Fields maps "path.serviceID", "query.q" and "body.git_ref" to their
	// JSON schemas.
	Fields map[string]map[string]any
}

// Tool is one MCP tool, built from x-toft-tools.
type Tool struct {
	Name        string
	Description string
	Scope       string
	Policy      string
	Operations  []string
	// Inputs maps each input to the operation field it fills ("" for one
	// with its own schema).
	Inputs map[string]string
	// Secret inputs are never recorded (D-102).
	Secret map[string]bool
	Schema map[string]any
}

// Spec is what the MCP server reads from the OpenAPI document.
type Spec struct {
	Ops   map[string]*Operation
	Tools []Tool
}

// LoadSpec reads the operations and the tools from an OpenAPI document. An
// input pointing at a field no operation has is an error: the spec and the
// tools have drifted.
func LoadSpec(raw []byte) (*Spec, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	s := &Spec{Ops: map[string]*Operation{}}
	paths, _ := doc["paths"].(map[string]any)
	for path, v := range paths {
		item, _ := v.(map[string]any)
		shared := params(doc, item["parameters"])
		for method, ov := range item {
			op, ok := ov.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				continue
			}
			o := &Operation{ID: id, Method: strings.ToUpper(method), Path: path, Fields: map[string]map[string]any{}}
			maps.Copy(o.Fields, shared)
			maps.Copy(o.Fields, params(doc, op["parameters"]))
			if body := dig(op, "requestBody", "content", "application/json", "schema"); body != nil {
				b := resolve(doc, body)
				props, _ := b["properties"].(map[string]any)
				for name, ps := range props {
					o.Fields["body."+name] = jsonSchema(doc, ps)
				}
			}
			s.Ops[id] = o
		}
	}
	list, _ := doc["x-toft-tools"].([]any)
	for _, tv := range list {
		t, _ := tv.(map[string]any)
		tool := Tool{Inputs: map[string]string{}, Secret: map[string]bool{}}
		tool.Name, _ = t["name"].(string)
		tool.Description, _ = t["description"].(string)
		tool.Scope, _ = t["scope"].(string)
		tool.Policy, _ = t["policy"].(string)
		for _, o := range asList(t["operations"]) {
			id, _ := o.(string)
			if s.Ops[id] == nil {
				return nil, fmt.Errorf("tool %s: no operation %q in the spec", tool.Name, id)
			}
			tool.Operations = append(tool.Operations, id)
		}
		props := map[string]any{}
		required := []string{}
		inputs, _ := t["input"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(inputs)) {
			in, _ := inputs[name].(map[string]any)
			var schema map[string]any
			if from, _ := in["from"].(string); from != "" {
				opID, field, ok := strings.Cut(from, "#")
				if !ok || !slices.Contains(tool.Operations, opID) {
					return nil, fmt.Errorf("tool %s, input %s: %q isn't a field of one of its operations", tool.Name, name, from)
				}
				fs, ok := s.Ops[opID].Fields[field]
				if !ok {
					return nil, fmt.Errorf("tool %s, input %s: operation %s has no field %s", tool.Name, name, opID, field)
				}
				schema, tool.Inputs[name] = maps.Clone(fs), from
			} else if own, ok := in["schema"].(map[string]any); ok {
				schema, tool.Inputs[name] = jsonSchema(doc, own), ""
			} else {
				return nil, fmt.Errorf("tool %s, input %s: neither from nor schema", tool.Name, name)
			}
			props[name] = schema
			if req, _ := in["required"].(bool); req {
				required = append(required, name)
			}
			if sec, _ := in["secret"].(bool); sec {
				tool.Secret[name] = true
			}
		}
		tool.Schema = map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			tool.Schema["required"] = required
		}
		s.Tools = append(s.Tools, tool)
	}
	return s, nil
}

func asList(v any) []any { l, _ := v.([]any); return l }

func dig(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// resolve follows a local $ref.
func resolve(doc map[string]any, v any) map[string]any {
	m, _ := v.(map[string]any)
	for i := 0; i < 8 && m != nil; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		m = dig(doc, strings.Split(strings.TrimPrefix(ref, "#/"), "/")...)
	}
	return m
}

func params(doc map[string]any, v any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, pv := range asList(v) {
		p := resolve(doc, pv)
		name, _ := p["name"].(string)
		in, _ := p["in"].(string)
		if name == "" || in == "" {
			continue
		}
		s := jsonSchema(doc, p["schema"])
		if d, ok := p["description"].(string); ok && s["description"] == nil {
			s["description"] = d
		}
		out[in+"."+name] = s
	}
	return out
}

// jsonSchema turns an OpenAPI 3.0 schema into the JSON Schema an MCP tool
// declares: references resolved, nullable dropped, only the keywords a
// tool's input uses.
func jsonSchema(doc map[string]any, v any) map[string]any {
	m := resolve(doc, v)
	out := map[string]any{}
	for _, k := range []string{"type", "description", "enum", "default", "minimum", "maximum", "minLength", "maxLength", "pattern", "format"} {
		if x, ok := m[k]; ok {
			out[k] = x
		}
	}
	if items, ok := m["items"]; ok {
		out["items"] = jsonSchema(doc, items)
	}
	if out["type"] == nil {
		out["type"] = "string"
	}
	return out
}
