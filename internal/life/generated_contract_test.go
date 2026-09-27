package life

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecGeneratedBusinessClients(t *testing.T) {
	t.Run("openapi.bot-generated-business-client", func(t *testing.T) {
		assertGeneratedBusinessCalls(t)
		for _, invalid := range []bool{false, true} {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/api/catalog/sections" || r.URL.Query().Get("pageSize") != "100" || r.URL.Query().Get("semesterId") != "2" || r.URL.Query().Get("search") != "数学" {
					t.Errorf("unexpected request %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				if invalid {
					_, _ = io.WriteString(w, `{"data":[],"pagination":{"page":"wrong-type"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"data":[{"id":4,"jwId":42,"code":"MATH.01","course":{"namePrimary":"数学"}}],"pagination":{"page":1,"totalPages":1}}`)
			}))
			rows, err := NewClient(server.URL, server.Client()).SectionCandidates(context.Background(), "数学", 2)
			server.Close()
			if requests != 1 {
				t.Fatalf("requests=%d", requests)
			}
			if invalid {
				if err == nil || rows != nil {
					t.Fatalf("malformed response returned success: %#v %v", rows, err)
				}
			} else {
				if err != nil || len(rows) != 1 || rows[0]["code"] != "MATH.01" {
					t.Fatalf("typed projection lost fields: %#v %v", rows, err)
				}
			}
		}
	})
}

// Inspect all production commands, and match the response type at each actual
// generated call to its generated operation, rather than merely checking imports.
func assertGeneratedBusinessCalls(t *testing.T) {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	generated, err := parser.ParseFile(fset, filepath.Join(root, "internal/openapi/client.gen.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]map[string]bool{}
	ast.Inspect(generated, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || !strings.HasSuffix(spec.Name.Name, "Response") {
			return true
		}
		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		op := strings.TrimSuffix(spec.Name.Name, "Response")
		models[op] = map[string]bool{}
		for _, field := range st.Fields.List {
			if len(field.Names) == 1 && strings.HasPrefix(field.Names[0].Name, "JSON2") {
				if ptr, ok := field.Type.(*ast.StarExpr); ok {
					models[op][typeText(fset, ptr.X)] = true
				}
			}
		}
		return true
	})
	calls := 0
	err = filepath.WalkDir(filepath.Join(root, "internal/life"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		// Every generated operation call is directly consumed by the typed decoder.
		parents := map[ast.Node]ast.Node{}
		var stack []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			if len(stack) > 0 {
				parents[node] = stack[len(stack)-1]
			}
			stack = append(stack, node)
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if receiver, ok := method.X.(*ast.Ident); ok {
				if receiver.Name == "api" && (method.Sel.Name == "NewClient" || method.Sel.Name == "NewClientWithRefresh" || method.Sel.Name == "ReadResponse" || method.Sel.Name == "DecodeResponseBody") {
					t.Errorf("untyped business transport at %s", fset.Position(call.Pos()))
				}
				if receiver.Name == "http" && (method.Sel.Name == "NewRequest" || method.Sel.Name == "NewRequestWithContext" || method.Sel.Name == "Get" || method.Sel.Name == "Post") {
					t.Errorf("handwritten HTTP business request at %s", fset.Position(call.Pos()))
				}
			}
			if method.Sel.Name == "DoJSON" || method.Sel.Name == "DoRaw" || method.Sel.Name == "Do" {
				t.Errorf("raw business request at %s", fset.Position(call.Pos()))
			}
			op := method.Sel.Name
			for _, suffix := range []string{"WithFormdataBody", "WithBody"} {
				op = strings.TrimSuffix(op, suffix)
			}
			allowed, generatedCall := models[op]
			if !generatedCall {
				return true
			}
			calls++
			// Binary downloads have no generated JSON success model.
			if len(allowed) == 0 {
				return true
			}
			generic := generatedDecoder(call, parents)
			if generic == nil {
				t.Errorf("generated %s bypasses typed response at %s", op, fset.Position(call.Pos()))
				return true
			}

			actual := strings.ReplaceAll(typeText(fset, generic.Index), "openapi.", "")
			actual = strings.ReplaceAll(actual, "any", "interface{}")
			if !allowed[actual] {
				t.Errorf("%s decodes %s; declared responses %v", op, actual, allowed)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("no generated business operations inspected")
	}
}

func typeText(fset *token.FileSet, expr ast.Expr) string {
	var out bytes.Buffer
	_ = format.Node(&out, fset, expr)
	return out.String()
}

// Follow the operation result to a typed decoder in its lexical statement block.
// Direct decoder calls and page-fetch callbacks retain their explicit model too.
func generatedDecoder(call *ast.CallExpr, parents map[ast.Node]ast.Node) *ast.IndexExpr {
	for node := ast.Node(call); node != nil; node = parents[node] {
		if parent, ok := parents[node].(*ast.CallExpr); ok {
			if generic, ok := parent.Fun.(*ast.IndexExpr); ok {
				if name, ok := generic.X.(*ast.Ident); ok && (name.Name == "typedJSON" || name.Name == "typedResponse" || name.Name == "typedDataList" || name.Name == "catalogCandidates") {
					return generic
				}
			}
		}
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if len(assignment.Lhs) == 0 {
			return nil
		}
		result, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok {
			return nil
		}
		block, ok := parents[assignment].(*ast.BlockStmt)
		if !ok {
			return nil
		}
		seen := false
		for _, statement := range block.List {
			if statement == assignment {
				seen = true
				continue
			}
			if !seen {
				continue
			}
			var decoder *ast.IndexExpr
			overwritten := false
			ast.Inspect(statement, func(n ast.Node) bool {
				if a, ok := n.(*ast.AssignStmt); ok {
					for _, lhs := range a.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Name == result.Name {
							overwritten = true
						}
					}
				}
				next, ok := n.(*ast.CallExpr)
				if !ok || len(next.Args) == 0 {
					return true
				}
				generic, ok := next.Fun.(*ast.IndexExpr)
				if !ok {
					return true
				}
				name, ok := generic.X.(*ast.Ident)
				if !ok || (name.Name != "typedJSON" && name.Name != "typedDataList" && name.Name != "typedResponse") {
					return true
				}
				argument, ok := next.Args[0].(*ast.Ident)
				if ok && argument.Name == result.Name {
					decoder = generic
				}
				return true
			})
			if overwritten {
				return nil
			}
			if decoder != nil {
				return decoder
			}
		}
		return nil
	}
	return nil
}
