package specification

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/mod/modfile"
	"reflect"
	"sort"
	"strings"
)

type Summary struct {
	Commit       string    `json:"commit"`
	SpecSHA256   string    `json:"specSha256"`
	SchemaSHA256 string    `json:"schemaSha256"`
	SourceSHA256 string    `json:"sourceSha256"`
	Requirements int       `json:"requirements"`
	Cases        int       `json:"cases"`
	Receipts     []Receipt `json:"receipts"`
}

// Evidence is accepted only when declarations, fully consumed observations,
// and native Go pass events agree. A test name alone cannot prove a requirement.
func ValidateEvidence(catalog *Catalog, input io.Reader) (Summary, error) {
	summary := Summary{Commit: catalog.Commit, SpecSHA256: catalog.SpecSHA256, SchemaSHA256: catalog.SchemaSHA256, SourceSHA256: catalog.SourceSHA256, Requirements: len(catalog.Document.Requirements)}
	moduleFile, err := os.ReadFile(filepath.Join(catalog.Root, "go.mod"))
	if err != nil {
		return summary, err
	}
	module := modfile.ModulePath(moduleFile)
	if module == "" {
		return summary, fmt.Errorf("missing module path")
	}
	type stream struct{ Package, Test string }
	bindings := map[string]string{}
	declared := map[string]Requirement{}
	cases := map[string]Case{}
	for _, req := range catalog.Document.Requirements {
		declared[req.ID] = req
		bindings[req.Acceptance.Test.Name] = path.Join(module, path.Dir(req.Acceptance.Test.File))
		for _, c := range req.Expectations.Cases {
			cases[req.ID+"/"+c.ID] = c
		}
	}
	passed := map[stream]int{}
	run := map[stream]int{}
	receipts := map[string]Receipt{}
	consume := func(native stream, output string) error {
		marker := strings.Index(output, "SPEC_EVIDENCE ")
		if marker < 0 {
			return nil
		}
		var receipt Receipt
		if err := StrictDecode([]byte(strings.TrimSpace(output[marker+len("SPEC_EVIDENCE "):])), &receipt); err != nil {
			return err
		}
		key := receipt.Requirement + "/" + receipt.Case
		req, found := declared[receipt.Requirement]
		if !found {
			return fmt.Errorf("unknown requirement receipt %s", key)
		}
		c, found := cases[key]
		if !found {
			return fmt.Errorf("unknown case receipt %s", key)
		}
		if _, exists := receipts[key]; exists {
			return fmt.Errorf("duplicate receipt %s", key)
		}
		if native.Package != bindings[req.Acceptance.Test.Name] || native.Test != receipt.Test || receipt.Test != req.Acceptance.Test.Name+"/"+receipt.Case {
			return fmt.Errorf("receipt/native test mismatch %s", key)
		}
		if receipt.Commit != catalog.Commit || receipt.SpecSHA256 != catalog.SpecSHA256 || receipt.SchemaSHA256 != catalog.SchemaSHA256 || receipt.SourceSHA256 != catalog.SourceSHA256 || receipt.Kind != req.Expectations.Kind {
			return fmt.Errorf("stale or wrong receipt provenance %s", key)
		}
		var expected map[string]any
		if err := json.Unmarshal(c.Expected, &expected); err != nil {
			return err
		}
		fields := make([]string, 0, len(expected))
		for field := range expected {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		if !reflect.DeepEqual(fields, receipt.Consumed) {
			return fmt.Errorf("unconsumed or unknown expectation fields %s", key)
		}
		actual := observation(req.Expectations.Kind)
		if err := StrictDecode(receipt.Observed, actual); err != nil {
			return fmt.Errorf("missing or malformed observation %s: %w", key, err)
		}
		if err := matchObservation(req.Expectations.Kind, c.Expected, reflect.ValueOf(actual).Elem().Interface()); err != nil {
			return fmt.Errorf("observed result mismatch %s: %w", key, err)
		}
		if len(receipt.Sources) != len(req.Sources) {
			return fmt.Errorf("unconsumed sources %s", key)
		}
		for i, source := range req.Sources {
			witness := receipt.Sources[i]
			if witness.OperationID != source.OperationID || witness.Requests == 0 || witness.Responses+witness.InvalidResponses == 0 {
				return fmt.Errorf("missing source execution %s", key)
			}
			parameters := append([]string{}, source.Parameters...)
			sort.Strings(parameters)
			if !reflect.DeepEqual(parameters, witness.Parameters) {
				return fmt.Errorf("unconsumed source parameters %s", key)
			}
			if req.Expectations.Kind == "collection" {
				if float64(witness.Requests) != expected["requests"] {
					return fmt.Errorf("request validation count mismatch %s", key)
				}
				if count, ok := expected["rows"].(float64); ok && count > 0 {
					wanted := []string{}
					for _, field := range source.ResponseFields {
						if strings.HasPrefix(field, "/data/0/") {
							wanted = append(wanted, field)
						}
					}
					sort.Strings(wanted)
					if !reflect.DeepEqual(wanted, witness.ProjectedFields) {
						return fmt.Errorf("unconsumed projection source fields %s", key)
					}
				}
			}
		}
		receipts[key] = receipt
		return nil
	}
	pending := map[stream]string{}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Package string
			Action  string
			Test    string
			Output  string
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return summary, fmt.Errorf("invalid Go event: %w", err)
		}
		if event.Action == "fail" {
			return summary, fmt.Errorf("native execution failed: %s", event.Test)
		}
		key := stream{Package: event.Package, Test: event.Test}
		for _, req := range catalog.Document.Requirements {
			name := req.Acceptance.Test.Name
			if event.Test != name && !strings.HasPrefix(event.Test, name+"/") {
				continue
			}
			if event.Package != bindings[name] {
				return summary, fmt.Errorf("wrong native package for %s: %s", event.Test, event.Package)
			}
			if event.Test != name {
				caseID := strings.TrimPrefix(event.Test, name+"/")
				if _, ok := cases[req.ID+"/"+caseID]; !ok {
					return summary, fmt.Errorf("unknown native semantic case %s", event.Test)
				}
			}
			switch event.Action {
			case "skip":
				return summary, fmt.Errorf("canonical execution skipped: %s", event.Test)
			case "run":
				run[key]++
				if run[key] != 1 {
					return summary, fmt.Errorf("duplicate native run: %s", event.Test)
				}
			case "pass":
				passed[key]++
				if passed[key] != 1 {
					return summary, fmt.Errorf("duplicate native pass: %s", event.Test)
				}
			}
		}
		if event.Action != "output" {
			continue
		}
		output := pending[key] + event.Output
		for {
			line, rest, complete := strings.Cut(output, "\n")
			if !complete {
				break
			}
			if err := consume(key, line); err != nil {
				return summary, err
			}
			output = rest
		}
		if len(output) > 4*1024*1024 {
			return summary, fmt.Errorf("native output line too long: %s", event.Test)
		}
		pending[key] = output
	}
	if err := scanner.Err(); err != nil {
		return summary, err
	}
	for key, output := range pending {
		if strings.Contains(output, "SPEC_EVIDENCE ") {
			return summary, fmt.Errorf("unterminated evidence output: %s", key.Test)
		}
	}
	for _, req := range catalog.Document.Requirements {
		if native := (stream{Package: bindings[req.Acceptance.Test.Name], Test: req.Acceptance.Test.Name}); passed[native] != 1 || run[native] != 1 {
			return summary, fmt.Errorf("canonical test did not pass: %s", req.ID)
		}
		for _, c := range req.Expectations.Cases {
			key := req.ID + "/" + c.ID
			receipt, found := receipts[key]
			native := stream{Package: bindings[req.Acceptance.Test.Name], Test: receipt.Test}
			if !found || passed[native] != 1 || run[native] != 1 {
				return summary, fmt.Errorf("missing native case evidence %s", key)
			}
			summary.Receipts = append(summary.Receipts, receipt)
		}
	}
	summary.Cases = len(summary.Receipts)
	return summary, nil
}
