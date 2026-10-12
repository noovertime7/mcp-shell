package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Typed builds a Tool whose arguments are decoded into In. The input schema
// is derived from In's fields: the json tag gives the name (omitempty makes
// it optional) and the jsonschema tag gives the description.
func Typed[In any](name, desc string, fn func(ctx context.Context, in In) (string, error)) *Tool {
	t := reflect.TypeOf((*In)(nil)).Elem()
	schema := schemaFor(t)
	required, _ := schema["required"].([]string)
	return &Tool{
		Name:        name,
		Description: desc,
		InputSchema: schema,
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var present map[string]json.RawMessage
			if err := json.Unmarshal(args, &present); err != nil {
				return "", fmt.Errorf("arguments must be a JSON object: %v", err)
			}
			for _, r := range required {
				if _, ok := present[r]; !ok {
					return "", fmt.Errorf("missing required argument %q", r)
				}
			}
			var in In
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("invalid arguments: %v", err)
			}
			return fn(ctx, in)
		},
	}
}

// TypedCall builds a Tool whose arguments are decoded into In and returns a CallResult.
func TypedCall[In any](name, desc string, fn func(ctx context.Context, in In) (*CallResult, error)) *Tool {
	t := reflect.TypeOf((*In)(nil)).Elem()
	schema := schemaFor(t)
	required, _ := schema["required"].([]string)
	return &Tool{
		Name:        name,
		Description: desc,
		InputSchema: schema,
		CallHandler: func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
			var present map[string]json.RawMessage
			if err := json.Unmarshal(args, &present); err != nil {
				return nil, fmt.Errorf("arguments must be a JSON object: %v", err)
			}
			for _, r := range required {
				if _, ok := present[r]; !ok {
					return nil, fmt.Errorf("missing required argument %q", r)
				}
			}
			var in In
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("invalid arguments: %v", err)
			}
			return fn(ctx, in)
		},
	}
}

// schemaFor returns a JSON Schema for t (structs, strings, numbers, bools,
// slices and string-keyed maps).
func schemaFor(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fs := schemaFor(f.Type)
			if d := f.Tag.Get("jsonschema"); d != "" {
				fs["description"] = d
			}
			props[name] = fs
			if !strings.Contains(","+opts+",", ",omitempty,") {
				required = append(required, name)
			}
		}
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	return map[string]any{}
}
