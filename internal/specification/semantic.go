// Package specification binds native acceptance observations to closed product contracts.
package specification

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Document struct {
	Version      int           `json:"version"`
	Owner        string        `json:"owner"`
	Requirements []Requirement `json:"requirements"`
}
type Requirement struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Rationale  string `json:"rationale"`
	Acceptance struct {
		Given string   `json:"given"`
		When  string   `json:"when"`
		Then  []string `json:"then"`
		Test  struct {
			File string `json:"file"`
			Name string `json:"name"`
		} `json:"test"`
	} `json:"acceptance"`
	Sources      []Source     `json:"sources"`
	Expectations Expectations `json:"expectations"`
}
type Source struct {
	OperationID    string   `json:"operationId"`
	Parameters     []string `json:"parameters"`
	ResponseStatus string   `json:"responseStatus"`
	ResponseFields []string `json:"responseFields"`
}
type Expectations struct {
	Kind  string `json:"kind"`
	Cases []Case `json:"cases"`
}
type Case struct {
	ID       string          `json:"id"`
	Expected json.RawMessage `json:"expected"`
}

// These are finite observable product outcomes, not an expression/assertion language.
type Collection struct {
	Requests             int      `json:"requests"`
	Pages                []int    `json:"pages"`
	PageSizes            []int    `json:"pageSizes"`
	Rows                 int      `json:"rows"`
	UndatedRows          int      `json:"undatedRows"`
	ProjectionMismatches []string `json:"projectionMismatches"`
	Error                bool     `json:"error"`
	PartialRows          bool     `json:"partialRows"`
	Bearers              []string `json:"bearers"`
	SectionJwIDs         []string `json:"sectionJwIds"`
}
type Discovery struct {
	User             string   `json:"user"`
	Tools            []string `json:"tools"`
	SchemaEqual      bool     `json:"schemaEqual"`
	AnnotationsEqual bool     `json:"annotationsEqual"`
}
type Authorization struct {
	Bearer string `json:"bearer"`
	Calls  int    `json:"calls"`
	Error  bool   `json:"error"`
	Result string `json:"result"`
}
type SharedSurface struct {
	Conversation string   `json:"conversation"`
	Tools        []string `json:"tools"`
	MCPSession   bool     `json:"mcpSession"`
	MCPRequests  int      `json:"mcpRequests"`
}
type Confirmation struct {
	State               string `json:"state"`
	RemoteCalls         int    `json:"remoteCalls"`
	CallsBeforeApproval int    `json:"callsBeforeApproval"`
	ModelCalls          int    `json:"modelCalls"`
	DecisionRecorded    bool   `json:"decisionRecorded"`
	CrossJobRejected    bool   `json:"crossJobRejected"`
}
type RoomInput struct {
	Input       string `json:"input"`
	RoomCommand bool   `json:"roomCommand"`
	Code        string `json:"code"`
	Scope       string `json:"scope"`
}
type RoomPresentation struct {
	Text           string   `json:"text"`
	ImageURL       string   `json:"imageUrl"`
	Status         string   `json:"status"`
	Code           string   `json:"code"`
	Floor          string   `json:"floor"`
	Images         int      `json:"images"`
	ImageReference bool     `json:"imageReference"`
	RoomText       bool     `json:"roomText"`
	Requests       []string `json:"requests"`
}
type Architecture struct {
	Operations      int    `json:"operations"`
	RequestPageSize int    `json:"requestPageSize"`
	Rows            int    `json:"rows"`
	Code            string `json:"code"`
	Error           bool   `json:"error"`
}

func observation(kind string) any {
	switch kind {
	case "collection":
		return new(Collection)
	case "discovery":
		return new(Discovery)
	case "authorization":
		return new(Authorization)
	case "shared-surface":
		return new(SharedSurface)
	case "confirmation":
		return new(Confirmation)
	case "room-input":
		return new(RoomInput)
	case "room-presentation":
		return new(RoomPresentation)
	case "architecture":
		return new(Architecture)
	default:
		return nil
	}
}
func Root() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("repository go.mod not found")
		}
		dir = parent
	}
}
func Digest(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func StrictDecode(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}
func ValidateDocument(data, schemaData []byte) (Document, error) {
	var raw, schema any
	if err := json.Unmarshal(data, &raw); err != nil {
		return Document{}, err
	}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		return Document{}, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://life.local/bot-contracts.schema.json", schema); err != nil {
		return Document{}, err
	}
	compiled, err := compiler.Compile("https://life.local/bot-contracts.schema.json")
	if err != nil {
		return Document{}, err
	}
	if err := compiled.Validate(raw); err != nil {
		return Document{}, err
	}
	var doc Document
	if err := StrictDecode(data, &doc); err != nil {
		return doc, err
	}
	ids := map[string]bool{}
	for _, req := range doc.Requirements {
		if ids[req.ID] {
			return doc, fmt.Errorf("duplicate requirement %s", req.ID)
		}
		ids[req.ID] = true
		cases := map[string]bool{}
		for _, c := range req.Expectations.Cases {
			if cases[c.ID] {
				return doc, fmt.Errorf("duplicate case %s/%s", req.ID, c.ID)
			}
			cases[c.ID] = true
			target := observation(req.Expectations.Kind)
			if target == nil {
				return doc, fmt.Errorf("unknown kind %s", req.Expectations.Kind)
			}
			if err := StrictDecode(c.Expected, target); err != nil {
				return doc, err
			}
		}
	}
	return doc, nil
}

type Catalog struct {
	Document     Document
	SpecSHA256   string
	SchemaSHA256 string
	SourceSHA256 string
	Commit       string
	Root         string
}

func Load() (*Catalog, error) {
	root := Root()
	data, err := os.ReadFile(filepath.Join(root, "docs/specifications/contracts.json"))
	if err != nil {
		return nil, err
	}
	schema, err := os.ReadFile(filepath.Join(root, "docs/specifications/contracts.schema.json"))
	if err != nil {
		return nil, err
	}
	doc, err := ValidateDocument(data, schema)
	if err != nil {
		return nil, err
	}
	source, err := os.ReadFile(filepath.Join(root, "api/openapi.json"))
	if err != nil {
		return nil, err
	}
	if err := validateSourceReferences(doc, source); err != nil {
		return nil, err
	}
	provenance, err := os.ReadFile(filepath.Join(root, "api/openapi.provenance"))
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(provenance)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || values[key] != "" {
			return nil, fmt.Errorf("invalid OpenAPI provenance")
		}
		values[key] = value
	}
	if len(values) != 3 || values["repository"] != "Life-USTC/server" || values["sha256"] != Digest(source) || len(values["commit"]) != 40 {
		return nil, fmt.Errorf("OpenAPI provenance mismatch")
	}
	if _, err := hex.DecodeString(values["commit"]); err != nil {
		return nil, fmt.Errorf("invalid OpenAPI commit")
	}
	commit, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, err
	}
	return &Catalog{doc, Digest(data), Digest(schema), Digest(source), strings.TrimSpace(string(commit)), root}, nil
}

var cached struct {
	sync.Once
	catalog *Catalog
	err     error
}

func catalog(t *testing.T) *Catalog {
	t.Helper()
	cached.Do(func() { cached.catalog, cached.err = Load() })
	if cached.err != nil {
		t.Fatal(cached.err)
	}
	return cached.catalog
}

type Receipt struct {
	Requirement  string          `json:"requirement"`
	Case         string          `json:"case"`
	Test         string          `json:"test"`
	Kind         string          `json:"kind"`
	Commit       string          `json:"commit"`
	SpecSHA256   string          `json:"specSha256"`
	SchemaSHA256 string          `json:"schemaSha256"`
	SourceSHA256 string          `json:"sourceSha256"`
	Consumed     []string        `json:"consumed"`
	Sources      []SourceWitness `json:"sources"`
	Observed     json.RawMessage `json:"observed"`
}
type Run struct {
	t           *testing.T
	catalog     *Catalog
	Requirement Requirement
	consumed    map[string]bool
	mu          sync.Mutex
	wires       map[string]*Wire
}

func Begin(t *testing.T) *Run {
	t.Helper()
	cat := catalog(t)
	var req *Requirement
	for i := range cat.Document.Requirements {
		candidate := &cat.Document.Requirements[i]
		if t.Name() == candidate.Acceptance.Test.Name {
			req = candidate
			break
		}
	}
	if req == nil {
		t.Fatalf("no canonical requirement for %s", t.Name())
	}
	run := &Run{t: t, catalog: cat, Requirement: *req, consumed: map[string]bool{}, wires: map[string]*Wire{}}
	t.Cleanup(func() {
		for _, c := range req.Expectations.Cases {
			if !run.consumed[c.ID] {
				t.Errorf("unconsumed semantic case %s/%s", req.ID, c.ID)
			}
		}
	})
	return run
}
func (r *Run) Check(caseID string, actual any) {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed[caseID] {
		r.t.Fatalf("duplicate semantic case %s/%s", r.Requirement.ID, caseID)
	}
	var selected *Case
	for i := range r.Requirement.Expectations.Cases {
		c := &r.Requirement.Expectations.Cases[i]
		if c.ID == caseID {
			selected = c
			break
		}
	}
	if selected == nil {
		r.t.Fatalf("unknown semantic case %s/%s", r.Requirement.ID, caseID)
	}
	r.consumed[caseID] = true
	r.t.Run(caseID, func(t *testing.T) {
		if err := matchObservation(r.Requirement.Expectations.Kind, selected.Expected, actual); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(selected.Expected, &fields); err != nil {
			t.Fatal(err)
		}
		consumed := make([]string, 0, len(fields))
		for key := range fields {
			consumed = append(consumed, key)
		}
		sort.Strings(consumed)
		sources := []SourceWitness{}
		for _, source := range r.Requirement.Sources {
			wire := r.wires[source.OperationID]
			if wire == nil {
				t.Fatalf("unconsumed source %s", source.OperationID)
			}
			witness := wire.takeWitness()
			if witness.Requests == 0 || witness.Responses+witness.InvalidResponses == 0 {
				t.Fatalf("source %s lacks request/fixture execution", source.OperationID)
			}
			sources = append(sources, witness)
		}
		observed, err := json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
		receipt := Receipt{r.Requirement.ID, caseID, t.Name(), r.Requirement.Expectations.Kind, r.catalog.Commit, r.catalog.SpecSHA256, r.catalog.SchemaSHA256, r.catalog.SourceSHA256, consumed, sources, observed}
		encoded, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("SPEC_EVIDENCE " + string(encoded))
	})
}

func matchObservation(kind string, raw json.RawMessage, actual any) error {
	expected := observation(kind)
	if expected == nil {
		return fmt.Errorf("unknown kind %s", kind)
	}
	if reflect.TypeOf(actual) != reflect.TypeOf(expected).Elem() {
		return fmt.Errorf("kind %s cannot check %T", kind, actual)
	}
	if err := StrictDecode(raw, expected); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, reflect.ValueOf(expected).Elem().Interface()) {
		return fmt.Errorf("semantic mismatch: actual %+v expected %+v", actual, expected)
	}
	return nil
}
