package specification

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func semanticFiles(t *testing.T) ([]byte, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Root(), "docs/specifications/contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(Root(), "docs/specifications/contracts.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data, schema
}
func TestSemanticSchemaRejectsUnknownMissingAndDuplicateCases(t *testing.T) {
	data, schema := semanticFiles(t)
	mutations := map[string]func(map[string]any){
		"unknown-kind": func(r map[string]any) { r["expectations"].(map[string]any)["kind"] = "expression" },
		"unknown-field": func(r map[string]any) {
			r["expectations"].(map[string]any)["cases"].([]any)[0].(map[string]any)["expected"].(map[string]any)["unconsumed"] = true
		},
		"missing-field": func(r map[string]any) {
			delete(r["expectations"].(map[string]any)["cases"].([]any)[0].(map[string]any)["expected"].(map[string]any), "user")
		},
		"duplicate-case": func(r map[string]any) {
			e := r["expectations"].(map[string]any)
			e["cases"] = append(e["cases"].([]any), e["cases"].([]any)[0])
		},
		"empty-cases": func(r map[string]any) { r["expectations"].(map[string]any)["cases"] = []any{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			mutate(raw["requirements"].([]any)[0].(map[string]any))
			changed, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateDocument(changed, schema); err == nil {
				t.Fatal("invalid semantic declaration accepted")
			}
		})
	}
}
func TestSemanticExpectedValuesAreCompared(t *testing.T) {
	expected := []byte(`{"bearer":"Bearer owner","calls":1,"error":true,"result":""}`)
	actual := Authorization{Bearer: "Bearer owner", Calls: 1, Error: true}
	if err := matchObservation("authorization", expected, actual); err != nil {
		t.Fatal(err)
	}
	for _, changed := range [][]byte{bytes.Replace(expected, []byte(`"calls":1`), []byte(`"calls":2`), 1), bytes.Replace(expected, []byte(`"error":true`), []byte(`"error":false`), 1)} {
		if err := matchObservation("authorization", changed, actual); err == nil {
			t.Fatal("changing a semantic expectation did not fail")
		}
	}
}
func TestWireFixturesAndAuthoritativeBounds(t *testing.T) {
	source := Source{OperationID: "community_section_homework_list", Parameters: []string{"page", "pageSize", "sectionJwId"}, ResponseStatus: "200", ResponseFields: []string{"/data/0/id", "/data/0/completionRequired"}}
	wire := OpenAPI(t, source)
	fixture := wire.FixtureFile("section-homeworks.json")
	wire.Fixture(fixture)
	delete(fixture["data"].([]any)[0].(map[string]any), "id")
	if err := wire.Validate(fixture); err == nil {
		t.Fatal("missing required wire field accepted")
	}
	for _, query := range []string{"page=1&pageSize=100", "page=101&pageSize=50", "page=1&pageSize=50&limit=50"} {
		if err := wire.ValidateRequest(httptest.NewRequest("GET", "/api/community/section-homeworks?"+query, nil)); err == nil {
			t.Fatalf("invalid request accepted: %s", query)
		}
	}
	if err := wire.ValidateRequest(httptest.NewRequest("GET", "/api/community/section-homeworks?page=2&pageSize=50&sectionJwId=654", nil)); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []Source{{OperationID: "made-up", ResponseStatus: "200"}, {OperationID: source.OperationID, Parameters: []string{"limit"}, ResponseStatus: "200"}, {OperationID: source.OperationID, ResponseStatus: "200", ResponseFields: []string{"/data/0/missing"}}} {
		if _, err := resolveSource(wire.document, broken); err == nil {
			t.Fatalf("unknown source accepted: %+v", broken)
		}
	}
}
func TestEvidenceRejectsMissingUnknownStaleAndUnconsumedObservations(t *testing.T) {
	catalog := &Catalog{Root: Root(), Commit: "commit", SpecSHA256: "spec", SchemaSHA256: "schema", SourceSHA256: "source"}
	req := Requirement{ID: "test.authorization", Expectations: Expectations{Kind: "authorization", Cases: []Case{{ID: "denied", Expected: json.RawMessage(`{"bearer":"Bearer owner","calls":1,"error":true,"result":""}`)}}}}
	req.Acceptance.Test.Name = "TestNative/test.authorization"
	req.Acceptance.Test.File = "internal/specification/semantic_test.go"
	const nativePackage = "github.com/Life-USTC/Bot/internal/specification"
	catalog.Document.Requirements = []Requirement{req}
	original := Receipt{Requirement: req.ID, Case: "denied", Test: req.Acceptance.Test.Name + "/denied", Kind: "authorization", Commit: catalog.Commit, SpecSHA256: catalog.SpecSHA256, SchemaSHA256: catalog.SchemaSHA256, SourceSHA256: catalog.SourceSHA256, Consumed: []string{"bearer", "calls", "error", "result"}, Sources: []SourceWitness{}, Observed: req.Expectations.Cases[0].Expected}
	native := func(receipts []Receipt, pass bool, extra string) string {
		events := []map[string]any{{"Package": nativePackage, "Action": "run", "Test": req.Acceptance.Test.Name}, {"Package": nativePackage, "Action": "run", "Test": original.Test}}
		for _, receipt := range receipts {
			encoded, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, map[string]any{"Package": nativePackage, "Action": "output", "Test": receipt.Test, "Output": "SPEC_EVIDENCE " + string(encoded) + "\n"})
		}
		if pass {
			events = append(events, map[string]any{"Package": nativePackage, "Action": "pass", "Test": original.Test}, map[string]any{"Package": nativePackage, "Action": "pass", "Test": req.Acceptance.Test.Name})
		}
		if extra != "" {
			events = append(events, map[string]any{"Package": nativePackage, "Action": "pass", "Test": req.Acceptance.Test.Name + "/" + extra})
		}
		var lines []string
		for _, event := range events {
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(encoded))
		}
		return strings.Join(lines, "\n")
	}
	if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{original}, true, ""))); err != nil {
		t.Fatal(err)
	}
	t.Run("chunked-native-output", func(t *testing.T) {
		complete := native([]Receipt{original}, true, "")
		lines := strings.Split(complete, "\n")
		first := lines[2]
		rest := strings.Join(lines[:2], "\n") + "\n" + strings.Join(lines[3:], "\n")
		var event map[string]any
		if err := json.Unmarshal([]byte(first), &event); err != nil {
			t.Fatal(err)
		}
		output := event["Output"].(string)
		var chunked strings.Builder
		// Split even the marker across native output events, as test2json can do.
		for _, chunk := range []string{output[:7], output[7:41], output[41:]} {
			event["Output"] = chunk
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			chunked.Write(encoded)
			chunked.WriteByte('\n')
		}
		chunked.WriteString(rest)
		if _, err := ValidateEvidence(catalog, strings.NewReader(chunked.String())); err != nil {
			t.Fatal(err)
		}
		event["Output"] = strings.TrimSuffix(output, "\n")
		truncated, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateEvidence(catalog, strings.NewReader(string(truncated)+"\n"+rest)); err == nil {
			t.Fatal("unterminated evidence accepted")
		}
	})

	mutations := map[string]func(*Receipt){"wrong-observation": func(r *Receipt) { r.Observed = bytes.Replace(r.Observed, []byte(`"calls":1`), []byte(`"calls":2`), 1) }, "stale-commit": func(r *Receipt) { r.Commit = "old" }, "stale-spec": func(r *Receipt) { r.SpecSHA256 = "old" }, "stale-schema": func(r *Receipt) { r.SchemaSHA256 = "old" }, "stale-source": func(r *Receipt) { r.SourceSHA256 = "old" }, "unknown-case": func(r *Receipt) { r.Case = "unknown" }, "unconsumed-field": func(r *Receipt) { r.Consumed = r.Consumed[:3] }, "unknown-field": func(r *Receipt) { r.Consumed = append(r.Consumed, "unknown") }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			receipt := original
			mutate(&receipt)
			if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{receipt}, true, ""))); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	for name, events := range map[string]string{"missing": native(nil, true, ""), "not-passed": native([]Receipt{original}, false, ""), "duplicate": native([]Receipt{original, original}, true, ""), "unknown-native-case": native([]Receipt{original}, true, "other")} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateEvidence(catalog, strings.NewReader(events)); err == nil {
				t.Fatal("invalid native evidence accepted")
			}
		})
	}
	for _, action := range []string{"run", "pass", "skip"} {
		t.Run("unknown-native-"+action, func(t *testing.T) {
			event, _ := json.Marshal(map[string]any{"Package": nativePackage, "Action": action, "Test": req.Acceptance.Test.Name + "/unknown"})
			if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{original}, true, "")+"\n"+string(event))); err == nil {
				t.Fatal("unknown native case accepted")
			}
		})
	}
	for _, test := range []string{req.Acceptance.Test.Name, original.Test} {
		for _, action := range []string{"run", "pass"} {
			event, _ := json.Marshal(map[string]any{"Package": nativePackage, "Action": action, "Test": test})
			if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{original}, true, "")+"\n"+string(event))); err == nil {
				t.Fatalf("duplicate %s accepted for %s", action, test)
			}
		}
	}
	t.Run("wrong-native-package", func(t *testing.T) {
		wrong := strings.ReplaceAll(native([]Receipt{original}, true, ""), nativePackage, "github.com/Life-USTC/Bot/wrong")
		if _, err := ValidateEvidence(catalog, strings.NewReader(wrong)); err == nil {
			t.Fatal("wrong package accepted")
		}
	})

	req.Sources = []Source{{OperationID: "catalog_rooms_map", Parameters: []string{"code"}, ResponseStatus: "200", ResponseFields: []string{"/code"}}}
	catalog.Document.Requirements = []Requirement{req}
	withWire := original
	withWire.Sources = []SourceWitness{{OperationID: "catalog_rooms_map", Requests: 1, Responses: 1, Parameters: []string{"code"}, ProjectedFields: []string{}}}
	if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{withWire}, true, ""))); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"missing-request", "missing-response", "missing-parameter", "wrong-source"} {
		t.Run(failure, func(t *testing.T) {
			receipt := withWire
			receipt.Sources = append([]SourceWitness{}, withWire.Sources...)
			switch failure {
			case "missing-request":
				receipt.Sources[0].Requests = 0
			case "missing-response":
				receipt.Sources[0].Responses = 0
			case "missing-parameter":
				receipt.Sources[0].Parameters = []string{}
			case "wrong-source":
				receipt.Sources[0].OperationID = "other"
			}
			if _, err := ValidateEvidence(catalog, strings.NewReader(native([]Receipt{receipt}, true, ""))); err == nil {
				t.Fatal("unconsumed wire source accepted")
			}
		})
	}

}
