package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// campusCatalogTTL bounds how stale a cached tool catalog may be. Tool
// definitions are deployment configuration rather than user data, so caching
// them process-wide keeps the previous laziness benefit: a turn pays OAuth and
// tools/list only when the catalog has expired, not on every request.
const campusCatalogTTL = 10 * time.Minute

// campusToolEffect classifies a remote tool from the annotations the server
// derives from its own OAuth scope registry. That registry is the same
// authority that enforces access, so it is the source of truth rather than a
// hint to be second-guessed; keeping a hand-written copy in the host is how the
// weather location names drifted apart in the first place.
type campusToolEffect string

const (
	campusEffectRead        campusToolEffect = "read"
	campusEffectWrite       campusToolEffect = "write"
	campusEffectDestructive campusToolEffect = "destructive"
)

func campusEffectOf(tool mcpgo.Tool) campusToolEffect {
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		return campusEffectDestructive
	}
	if tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint {
		return campusEffectRead
	}
	// An unannotated tool is treated as a write: the host must not assume a
	// remote call is side-effect free just because it failed to say so.
	return campusEffectWrite
}

type campusCatalog struct {
	tools     []mcpgo.Tool
	fetchedAt time.Time
}

type campusCatalogCache struct {
	mu      sync.Mutex
	entries map[string]campusCatalog
	now     func() time.Time
}

func newCampusCatalogCache() *campusCatalogCache {
	return &campusCatalogCache{entries: make(map[string]campusCatalog), now: time.Now}
}

// get returns a cached catalog for one authorization class. Anonymous and
// authenticated listings are cached separately because the server filters the
// catalog by the caller's scopes: a group conversation has no user token and
// legitimately sees only the public tools.
func (c *campusCatalogCache) get(key string) ([]mcpgo.Tool, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || c.now().Sub(entry.fetchedAt) > campusCatalogTTL {
		return nil, false
	}
	return entry.tools, true
}

func (c *campusCatalogCache) put(key string, tools []mcpgo.Tool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = campusCatalog{tools: append([]mcpgo.Tool(nil), tools...), fetchedAt: c.now()}
}

// campusToolInfo converts a remote tool definition into the provider-facing
// tool. The server's own JSON Schema is passed through unchanged: its enums and
// descriptions are exactly the constraints the host has no way to reconstruct,
// and re-deriving them from a Go type would lose them.
func campusToolInfo(tool mcpgo.Tool) (name, description string, rawSchema json.RawMessage, err error) {
	schemaJSON, err := campusToolInputSchema(tool)
	if err != nil {
		return "", "", nil, err
	}
	description = strings.TrimSpace(tool.Description)
	switch campusEffectOf(tool) {
	case campusEffectWrite:
		description += "\nThis call changes campus data."
	case campusEffectDestructive:
		description += "\nThis call destroys campus data and is confirmed with the user before it runs."
	}
	return tool.Name, strings.TrimSpace(description), schemaJSON, nil
}

func campusToolInputSchema(candidate mcpgo.Tool) (json.RawMessage, error) {
	if len(candidate.RawInputSchema) > 0 {
		if !json.Valid(candidate.RawInputSchema) {
			return nil, fmt.Errorf("campus tool %s has an invalid input schema", candidate.Name)
		}
		return append(json.RawMessage(nil), candidate.RawInputSchema...), nil
	}
	data, err := json.Marshal(candidate.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("encode campus tool %s input schema: %w", candidate.Name, err)
	}
	return data, nil
}
