package specification

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type contractDocument struct {
	Version      int    `json:"version"`
	Owner        string `json:"owner"`
	Requirements []struct {
		ID         string `json:"id"`
		Scope      string `json:"scope"`
		Rule       string `json:"rule"`
		Acceptance struct {
			Given string   `json:"given"`
			When  string   `json:"when"`
			Then  []string `json:"then"`
			Test  struct {
				File string `json:"file"`
				Name string `json:"name"`
			} `json:"test"`
		} `json:"acceptance"`
	} `json:"requirements"`
}

// Native Go names include the TestSpec function and its one literal requirement
// subtest. This checks both directions; go test ./... executes the bound behavior.
func TestSpecificationBindings(t *testing.T) {
	root := filepath.Join("..", "..")
	file, err := os.Open(filepath.Join(root, "docs/specifications/contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var document contractDocument
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("expected one JSON document")
	}
	if document.Version != 1 || document.Owner == "" || len(document.Requirements) == 0 {
		t.Fatal("invalid specification header")
	}
	tests := map[string]string{}
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, declaration := range source.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "TestSpec") || fn.Name.Name == "TestSpecificationBindings" {
				continue
			}
			if fn.Body == nil || len(fn.Body.List) != 1 {
				t.Errorf("%s must contain one literal requirement subtest", fn.Name.Name)
				continue
			}
			statement, ok := fn.Body.List[0].(*ast.ExprStmt)
			if !ok {
				t.Errorf("%s has no subtest", fn.Name.Name)
				continue
			}
			call, ok := statement.X.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				t.Errorf("%s has no subtest", fn.Name.Name)
				continue
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Run" {
				t.Errorf("%s has no Run call", fn.Name.Name)
				continue
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Errorf("%s needs a literal ID", fn.Name.Name)
				continue
			}
			id, err := strconv.Unquote(literal.Value)
			if err != nil {
				return err
			}
			name := fn.Name.Name + "/" + id
			if _, exists := tests[name]; exists {
				t.Errorf("duplicate canonical test %s", name)
			}
			tests[name] = filepath.ToSlash(relative)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (method.Sel.Name == "Skip" || method.Sel.Name == "Skipf" || method.Sel.Name == "SkipNow") {
					t.Errorf("canonical test %s may not skip", name)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ids, owners := map[string]bool{}, map[string]bool{}
	for _, requirement := range document.Requirements {
		acceptance := requirement.Acceptance
		if requirement.ID == "" || ids[requirement.ID] {
			t.Errorf("invalid or repeated requirement ID %q", requirement.ID)
		}
		ids[requirement.ID] = true
		if requirement.Scope != "public" && requirement.Scope != "private" {
			t.Errorf("invalid scope for %s", requirement.ID)
		}
		if strings.TrimSpace(requirement.Rule) == "" || strings.TrimSpace(acceptance.Given) == "" || strings.TrimSpace(acceptance.When) == "" || len(acceptance.Then) == 0 {
			t.Errorf("incomplete acceptance for %s", requirement.ID)
		}
		for _, outcome := range acceptance.Then {
			if strings.TrimSpace(outcome) == "" {
				t.Errorf("empty outcome for %s", requirement.ID)
			}
		}
		binding := acceptance.Test
		if !strings.HasSuffix(binding.Name, "/"+requirement.ID) || tests[binding.Name] != binding.File || binding.File == "" {
			t.Errorf("missing canonical test for %s: %+v", requirement.ID, binding)
		}
		if owners[binding.Name] {
			t.Errorf("test %s has multiple owners", binding.Name)
		}
		owners[binding.Name] = true
	}
	for name := range tests {
		if !owners[name] {
			t.Errorf("orphan canonical test %s", name)
		}
	}
}
