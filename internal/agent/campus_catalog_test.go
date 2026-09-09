package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func boolPointer(value bool) *bool { return &value }

func TestCampusToolEffectComesFromServerAnnotations(t *testing.T) {
	// The server derives these annotations from the same OAuth scope registry
	// that enforces access, so they are the source of truth rather than a hint.
	read := mcpgo.Tool{Name: "catalog_weather_get"}
	read.Annotations.ReadOnlyHint = boolPointer(true)
	if got := campusEffectOf(read); got != campusEffectRead {
		t.Fatalf("read effect = %q", got)
	}
	write := mcpgo.Tool{Name: "workspace_todo_create"}
	write.Annotations.ReadOnlyHint = boolPointer(false)
	if got := campusEffectOf(write); got != campusEffectWrite {
		t.Fatalf("write effect = %q", got)
	}
	destructive := mcpgo.Tool{Name: "workspace_todo_delete"}
	destructive.Annotations.ReadOnlyHint = boolPointer(false)
	destructive.Annotations.DestructiveHint = boolPointer(true)
	if got := campusEffectOf(destructive); got != campusEffectDestructive {
		t.Fatalf("destructive effect = %q", got)
	}
	// Silence is not a promise of safety.
	if got := campusEffectOf(mcpgo.Tool{Name: "mystery"}); got != campusEffectWrite {
		t.Fatalf("unannotated effect = %q, want write", got)
	}
}

func TestCampusToolKeepsTheServerSchemaVerbatim(t *testing.T) {
	// The enum is the whole point: the Bot's own weather capability accepted
	// 高新 but rejected 高新区 with nothing written down, while the server
	// publishes the exact accepted values. Re-deriving the schema here would
	// lose them again.
	remote := mcpgo.Tool{
		Name:        "catalog_weather_get",
		Description: "Current conditions for one USTC campus location.",
		RawInputSchema: json.RawMessage(`{"type":"object","properties":{"locationKey":{"type":"string",` +
			`"enum":["ustc-main","ustc-gaoxin"],"default":"ustc-main","description":"USTC campus location."}}}`),
	}
	remote.Annotations.ReadOnlyHint = boolPointer(true)

	name, description, rawSchema, err := campusToolInfo(remote)
	if err != nil {
		t.Fatal(err)
	}
	if name != "catalog_weather_get" {
		t.Fatalf("name = %q", name)
	}
	if strings.Contains(description, "changes campus data") {
		t.Fatalf("a read-only tool must not be described as a write: %q", description)
	}
	if !json.Valid(rawSchema) || !strings.Contains(string(rawSchema), `"ustc-gaoxin"`) {
		t.Fatalf("schema lost the server's enum: %s", rawSchema)
	}
}

func TestCampusToolDescriptionMarksWrites(t *testing.T) {
	write := mcpgo.Tool{Name: "workspace_todo_create", Description: "Create a todo."}
	write.Annotations.ReadOnlyHint = boolPointer(false)
	_, description, _, err := campusToolInfo(write)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(description, "changes campus data") {
		t.Fatalf("write description = %q", description)
	}
}

func TestCampusCatalogCacheExpires(t *testing.T) {
	// Registering remote tools natively means listing them before the first
	// model request. The cache is what keeps that from costing an OAuth
	// exchange and a tools/list on every turn.
	now := time.Now()
	cache := newCampusCatalogCache()
	cache.now = func() time.Time { return now }
	cache.put("authenticated", []mcpgo.Tool{{Name: "catalog_weather_get"}})

	if tools, found := cache.get("authenticated"); !found || len(tools) != 1 {
		t.Fatalf("fresh entry = %#v found=%v", tools, found)
	}
	if _, found := cache.get("anonymous"); found {
		t.Fatal("an anonymous caller must not read the authenticated catalog")
	}
	now = now.Add(campusCatalogTTL + time.Second)
	if _, found := cache.get("authenticated"); found {
		t.Fatal("expired entry was served")
	}
}
