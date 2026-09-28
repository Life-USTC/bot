package specification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

type Wire struct {
	t         *testing.T
	source    Source
	document  *openapi3.T
	operation *openapi3.Operation
	path      string
	method    string
	schema    *openapi3.Schema
	mu        sync.Mutex
	witness   SourceWitness
}

type SourceWitness struct {
	OperationID      string   `json:"operationId"`
	Requests         int      `json:"requests"`
	Responses        int      `json:"responses"`
	InvalidResponses int      `json:"invalidResponses"`
	ProjectedFields  []string `json:"projectedFields"`
	Parameters       []string `json:"parameters"`
}

func OpenAPI(t *testing.T, source Source) *Wire {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join(Root(), "api/openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := resolveSource(document, source)
	if err != nil {
		t.Fatal(err)
	}
	wire.t = t
	return wire
}
func resolveSource(document *openapi3.T, source Source) (*Wire, error) {
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			if operation.OperationID == source.OperationID {
				response := operation.Responses.Value(source.ResponseStatus)
				if response == nil || response.Value.Content.Get("application/json") == nil {
					return nil, fmt.Errorf("source %s has no JSON response %s", source.OperationID, source.ResponseStatus)
				}
				wire := &Wire{source: source, document: document, operation: operation, path: path, method: method, schema: response.Value.Content.Get("application/json").Schema.Value}
				for _, parameter := range source.Parameters {
					if operation.Parameters.GetByInAndName("query", parameter) == nil && operation.Parameters.GetByInAndName("path", parameter) == nil {
						return nil, fmt.Errorf("unknown source parameter %s/%s", source.OperationID, parameter)
					}
				}
				for _, field := range source.ResponseFields {
					if err := schemaField(wire.schema, field); err != nil {
						return nil, err
					}
				}
				return wire, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown OpenAPI operation %s", source.OperationID)
}
func (r *Run) Wire(operationID string) *Wire {
	r.t.Helper()
	if wire := r.wires[operationID]; wire != nil {
		return wire
	}
	for _, source := range r.Requirement.Sources {
		if source.OperationID == operationID {
			wire := OpenAPI(r.t, source)
			r.wires[operationID] = wire
			return wire
		}
	}
	r.t.Fatalf("undeclared operation %s", operationID)
	return nil
}
func (w *Wire) takeWitness() SourceWitness {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.witness.OperationID = w.source.OperationID
	if w.witness.ProjectedFields == nil {
		w.witness.ProjectedFields = []string{}
	}
	sort.Strings(w.witness.ProjectedFields)
	if w.witness.Parameters == nil {
		w.witness.Parameters = []string{}
	}
	sort.Strings(w.witness.Parameters)
	result := w.witness
	w.witness = SourceWitness{}
	return result
}
func schemaField(schema *openapi3.Schema, pointer string) error {
	if !strings.HasPrefix(pointer, "/") {
		return fmt.Errorf("response field must be JSON pointer: %s", pointer)
	}
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		for len(schema.AllOf) == 1 {
			schema = schema.AllOf[0].Value
		}
		if schema.Items != nil {
			if _, err := strconv.Atoi(part); err != nil {
				return fmt.Errorf("array field %s needs an index", pointer)
			}
			schema = schema.Items.Value
			continue
		}
		property := schema.Properties[part]
		if property == nil {
			return fmt.Errorf("unknown response field %s", pointer)
		}
		schema = property.Value
	}
	return nil
}
func (w *Wire) Request(r *http.Request) {
	w.t.Helper()
	if err := w.ValidateRequest(r); err != nil {
		w.t.Errorf("request violates %s: %v", w.source.OperationID, err)
		return
	}
	w.mu.Lock()
	w.witness.Requests++
	for _, parameter := range w.source.Parameters {
		present := r.URL.Query().Has(parameter) || strings.Contains(w.path, "{"+parameter+"}")
		if present && !slices.Contains(w.witness.Parameters, parameter) {
			w.witness.Parameters = append(w.witness.Parameters, parameter)
		}
	}
	w.mu.Unlock()
}
func (w *Wire) ValidateRequest(r *http.Request) error {
	if r.Method != w.method {
		return fmt.Errorf("method=%s expected=%s", r.Method, w.method)
	}
	pathParams := map[string]string{}
	wantParts, gotParts := strings.Split(w.path, "/"), strings.Split(r.URL.Path, "/")
	if len(wantParts) != len(gotParts) {
		return fmt.Errorf("wrong source path %s", r.URL.Path)
	}
	for i, part := range wantParts {
		if strings.HasPrefix(part, "{") {
			pathParams[strings.Trim(part, "{}")] = gotParts[i]
		} else if part != gotParts[i] {
			return fmt.Errorf("wrong source path %s", r.URL.Path)
		}
	}
	for key := range r.URL.Query() {
		if w.operation.Parameters.GetByInAndName("query", key) == nil {
			return fmt.Errorf("unknown wire query parameter %s", key)
		}
	}
	input := &openapi3filter.RequestValidationInput{Request: r, PathParams: pathParams, Route: &routers.Route{Spec: w.document, Path: w.path, PathItem: w.document.Paths.Value(w.path), Method: w.method, Operation: w.operation}, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, SkipSettingDefaults: true}}
	return openapi3filter.ValidateRequest(context.Background(), input)
}
func (w *Wire) Fixture(value any) {
	w.t.Helper()
	if err := w.Validate(value); err != nil {
		w.t.Fatalf("fixture violates %s: %v", w.source.OperationID, err)
	}
	w.mu.Lock()
	w.witness.Responses++
	w.mu.Unlock()
}
func (w *Wire) InvalidFixture(value any) {
	w.t.Helper()
	if err := w.Validate(value); err == nil {
		w.t.Fatal("malformed fixture unexpectedly satisfies source schema")
	}
	w.mu.Lock()
	w.witness.InvalidResponses++
	w.mu.Unlock()
}
func (w *Wire) Validate(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return w.schema.VisitJSON(raw, openapi3.VisitAsResponse(), openapi3.EnableFormatValidation())
}
func (w *Wire) FixtureFile(name string) map[string]any {
	w.t.Helper()
	data, err := os.ReadFile(filepath.Join(Root(), "docs/specifications/fixtures", name))
	if err != nil {
		w.t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		w.t.Fatal(err)
	}
	return value
}

// Projection checks the referenced response fields for every returned row. The
// source is the independently validated HTTP fixture, before the client decodes it.
func (w *Wire) Projection(wireRows, rows []map[string]any) []string {
	mismatches := []string{}
	w.mu.Lock()
	for _, field := range w.source.ResponseFields {
		if strings.HasPrefix(field, "/data/0/") {
			w.witness.ProjectedFields = append(w.witness.ProjectedFields, field)
		}
	}
	w.mu.Unlock()
	if len(wireRows) != len(rows) {
		return []string{"row-count"}
	}
	for i, want := range wireRows {
		for _, field := range w.source.ResponseFields {
			if !strings.HasPrefix(field, "/data/0/") {
				continue
			}
			path := strings.TrimPrefix(field, "/data/0/")
			expected, ok1 := fieldValue(want, path)
			actual, ok2 := fieldValue(rows[i], path)
			if !ok1 || !ok2 || !reflect.DeepEqual(expected, actual) {
				mismatches = append(mismatches, fmt.Sprintf("row[%d]%s", i, field))
			}
		}
	}
	return mismatches
}
func fieldValue(value any, path string) (any, bool) {
	for _, part := range strings.Split(path, "/") {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(v) {
				return nil, false
			}
			value = v[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func validateSourceReferences(document Document, source []byte) error {
	schema, err := openapi3.NewLoader().LoadFromData(source)
	if err != nil {
		return err
	}
	for _, req := range document.Requirements {
		seen := map[string]bool{}
		for _, ref := range req.Sources {
			if seen[ref.OperationID] {
				return fmt.Errorf("duplicate source %s/%s", req.ID, ref.OperationID)
			}
			seen[ref.OperationID] = true
			if _, err := resolveSource(schema, ref); err != nil {
				return fmt.Errorf("%s: %w", req.ID, err)
			}
		}
	}
	return nil
}
