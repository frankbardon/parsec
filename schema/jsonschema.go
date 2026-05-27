package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// JSONSchema is a lightweight subset of JSON Schema (draft 2020-12)
// sufficient for envelope payload validation in Parsec. It supports:
//
//   - type:        string | number | integer | boolean | object | array | null
//   - required:    list of object property names
//   - properties:  per-property nested JSONSchema
//   - items:       schema for array elements
//   - additionalProperties: whether unknown object fields are allowed
//   - enum:        list of allowed literal values
//
// Anything richer (oneOf, allOf, $ref, regex patterns) is intentionally
// out of scope — Parsec subscribers should treat unknown fields as opaque
// and decode strictly via Go/TS code generation when stronger guarantees
// are required. A full JSON Schema validator can be substituted later
// without changing the Registry API.
type JSONSchema struct {
	Type                 string                 `json:"type,omitempty"`
	Required             []string               `json:"required,omitempty"`
	Properties           map[string]*JSONSchema `json:"properties,omitempty"`
	Items                *JSONSchema            `json:"items,omitempty"`
	AdditionalProperties *bool                  `json:"additionalProperties,omitempty"`
	Enum                 []any                  `json:"enum,omitempty"`
	Description          string                 `json:"description,omitempty"`
}

// Validate reports whether v conforms to s. The error is nil on success
// and a human-readable explanation otherwise. v is typically the result
// of json.Unmarshal on the envelope's payload.
func (s *JSONSchema) Validate(v any) error {
	if s == nil {
		return nil
	}
	return validateValue(s, v, "")
}

// ValidateBytes is a convenience that unmarshals raw and runs Validate.
func (s *JSONSchema) ValidateBytes(raw []byte) error {
	if s == nil {
		return nil
	}
	if len(raw) == 0 {
		return validateValue(s, nil, "")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("schema: payload is not JSON: %w", err)
	}
	return validateValue(s, v, "")
}

func validateValue(s *JSONSchema, v any, path string) error {
	if s == nil {
		return nil
	}
	if s.Type != "" {
		if err := checkType(s.Type, v, path); err != nil {
			return err
		}
	}
	if len(s.Enum) > 0 {
		matched := false
		for _, allowed := range s.Enum {
			if reflect.DeepEqual(allowed, v) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("schema: %s not in enum", displayPath(path))
		}
	}
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok && v != nil {
			return fmt.Errorf("schema: %s expected object", displayPath(path))
		}
		for _, k := range s.Required {
			if _, present := obj[k]; !present {
				return fmt.Errorf("schema: missing required field %s.%s", displayPath(path), k)
			}
		}
		for k, val := range obj {
			sub, ok := s.Properties[k]
			if !ok {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("schema: unexpected field %s.%s", displayPath(path), k)
				}
				continue
			}
			if err := validateValue(sub, val, path+"."+k); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok && v != nil {
			return fmt.Errorf("schema: %s expected array", displayPath(path))
		}
		if s.Items != nil {
			for i, e := range arr {
				if err := validateValue(s.Items, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func checkType(typ string, v any, path string) error {
	switch typ {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("schema: %s expected string", displayPath(path))
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("schema: %s expected number", displayPath(path))
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("schema: %s expected integer", displayPath(path))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("schema: %s expected boolean", displayPath(path))
		}
	case "object":
		if v == nil {
			return nil
		}
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("schema: %s expected object", displayPath(path))
		}
	case "array":
		if v == nil {
			return nil
		}
		if _, ok := v.([]any); !ok {
			return fmt.Errorf("schema: %s expected array", displayPath(path))
		}
	case "null":
		if v != nil {
			return fmt.Errorf("schema: %s expected null", displayPath(path))
		}
	}
	return nil
}

func displayPath(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}
