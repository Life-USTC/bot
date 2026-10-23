package agent

import (
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestCampusSearchRanksHomeworkActionAboveNestedOutputFields(t *testing.T) {
	// Shared response envelopes include homework and createdAt even for tools
	// whose actions have nothing to do with creating homework.
	output := json.RawMessage(`{"type":"object","properties":{"data":{"type":"object","properties":{"homework":{"type":"object","properties":{"createdAt":{"type":"string"}}}}}}}`)
	input := json.RawMessage(`{"type":"object","properties":{"sectionJwId":{"type":"string"},"title":{"type":"string"}},"required":["sectionJwId","title"]}`)
	ordinaryWrite := false
	create := mcpgo.Tool{
		Name: "community_section_homework_create", Title: "Create section homework",
		Description:    "Create a homework item for a section.",
		RawInputSchema: input, RawOutputSchema: output,
		Annotations: mcpgo.ToolAnnotation{ReadOnlyHint: &ordinaryWrite, DestructiveHint: &ordinaryWrite},
	}
	session := &lazyMCPSession{tools: map[string]mcpgo.Tool{create.Name: create}}
	session.once.Do(func() {})
	for _, name := range []string{"community_comment_create", "community_comment_get", "community_comment_list", "community_description_get", "community_description_set"} {
		session.tools[name] = mcpgo.Tool{Name: name, RawOutputSchema: output}
	}
	for _, query := range []string{"create homework", "手动创建作业", "新增课程作业"} {
		t.Run(query, func(t *testing.T) {
			result, err := session.search(t.Context(), campusToolSearchInput{Query: query})
			if err != nil {
				t.Fatal(err)
			}
			var docs []campusToolDocumentation
			if err := json.Unmarshal([]byte(result), &docs); err != nil {
				t.Fatal(err)
			}
			if len(docs) == 0 || docs[0].Name != create.Name {
				names := make([]string, len(docs))
				for i, doc := range docs {
					names[i] = doc.Name
				}
				t.Fatalf("homework creation must rank first, got %v", names)
			}
			if string(docs[0].InputSchema) != string(input) || string(docs[0].OutputSchema) != string(output) || docs[0].Effect != campusEffectWrite {
				t.Fatalf("discovery changed the tool contract: %+v", docs[0])
			}
		})
	}
	// Parameter-only searches still find the tool's complete input contract.
	result, err := session.search(t.Context(), campusToolSearchInput{Query: "sectionJwId"})
	if err != nil {
		t.Fatal(err)
	}
	var docs []campusToolDocumentation
	if err := json.Unmarshal([]byte(result), &docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Name != create.Name {
		t.Fatalf("parameter search lost homework creation: %+v", docs)
	}
}
