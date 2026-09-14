package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactSubscriptionBatchResultUsesResolvedTargetsAndExactIDs(t *testing.T) {
	const unrelatedMarker = "unrelated-subscription-metadata"
	matches := map[string]any{
		"action":                 "add",
		"semester":               map[string]any{"id": float64(1), "jwId": float64(202601), "nameCn": "2026年春季学期"},
		"matchedCodes":           []any{"MATH1006"},
		"unmatchedCodes":         []any{},
		"matchedSectionIds":      []any{float64(89780), float64(89781)},
		"addedCount":             float64(2),
		"alreadySubscribedCount": float64(0),
		"sections": []any{
			map[string]any{
				"id": float64(89780), "jwId": float64(20001), "code": "MATH1006.01",
				"course": map[string]any{
					"id": float64(5001), "jwId": float64(6001), "code": "MATH1006", "namePrimary": "数学分析",
					"metadata": strings.Repeat(unrelatedMarker, 100),
				},
				"semester": map[string]any{"id": float64(1), "jwId": float64(202601), "nameCn": "2026年春季学期"},
				"teachers": []any{map[string]any{"id": float64(21), "jwId": float64(21001), "code": "T001", "namePrimary": "目标老师"}},
			},
			map[string]any{
				"id": float64(89781), "jwId": float64(20002), "code": "MATH1006.02",
				"course":   map[string]any{"id": float64(5001), "jwId": float64(6001), "code": "MATH1006", "namePrimary": "数学分析"},
				"semester": map[string]any{"id": float64(1), "jwId": float64(202601), "nameCn": "2026年春季学期"},
			},
		},
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{"id": float64(89780), "jwId": float64(20001), "code": "MATH1006.01", "kind": "regular"},
				map[string]any{"id": float64(89781), "jwId": float64(20002), "code": "MATH1006.02", "kind": "regular"},
				// Same code, different semester: code matching would leak this entry.
				map[string]any{
					"id": float64(20276), "jwId": float64(30001), "code": "MATH1006.01", "kind": "auditor",
					"semester": map[string]any{"id": float64(2), "jwId": float64(202101), "nameCn": "2021年春季学期"},
					"course":   map[string]any{"namePrimary": unrelatedMarker},
				},
			},
		},
	}

	data := compactSubscriptionMutationData("subscribe", []string{"MATH1006"}, 0, matches)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), unrelatedMarker) {
		t.Fatalf("compact mutation data leaked redundant metadata: %s", encoded)
	}

	compact, ok := data["matches"].(map[string]any)
	if !ok {
		t.Fatalf("matches = %#v", data["matches"])
	}
	if compact["action"] != "add" || compact["addedCount"] != float64(2) || compact["alreadySubscribedCount"] != float64(0) {
		t.Fatalf("operation fields = %#v", compact)
	}
	sections, ok := compact["sections"].([]map[string]any)
	if !ok || len(sections) != 2 || sections[0]["code"] != "MATH1006.01" || sections[1]["code"] != "MATH1006.02" {
		t.Fatalf("resolved target sections = %#v", compact["sections"])
	}
	if sections[0]["id"] != float64(89780) || sections[0]["jwId"] != float64(20001) {
		t.Fatalf("target identity = %#v", sections[0])
	}
	if course, ok := sections[0]["course"].(map[string]any); !ok || course["jwId"] != float64(6001) || course["namePrimary"] != "数学分析" {
		t.Fatalf("target course = %#v", sections[0]["course"])
	}
	if semester, ok := sections[0]["semester"].(map[string]any); !ok || semester["jwId"] != float64(202601) {
		t.Fatalf("target semester = %#v", sections[0]["semester"])
	}
	if teachers, ok := sections[0]["teachers"].([]map[string]any); !ok || len(teachers) != 1 || teachers[0]["jwId"] != float64(21001) {
		t.Fatalf("target teachers = %#v", sections[0]["teachers"])
	}

	current, ok := compact["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("current subscription = %#v", compact["subscription"])
	}
	currentSections, ok := current["sections"].([]map[string]any)
	if !ok || len(currentSections) != 2 {
		t.Fatalf("current target membership = %#v", current["sections"])
	}
	for _, section := range currentSections {
		if section["id"] == float64(20276) {
			t.Fatalf("same-code section from another semester leaked: %#v", currentSections)
		}
	}
}

func TestCompactSubscriptionKindResultKeepsDocumentedFieldsOnly(t *testing.T) {
	result := map[string]any{
		"sectionJwId": float64(20001),
		"kind":        "teaching_assistant",
		"subscription": map[string]any{
			"sections": []any{map[string]any{"id": float64(89780), "code": "MATH1006.01"}},
		},
	}

	compact := compactSubscriptionKindResult(result)
	if len(compact) != 2 || compact["sectionJwId"] != float64(20001) || compact["kind"] != "teaching_assistant" {
		t.Fatalf("kind result = %#v", compact)
	}
	if _, ok := compact["subscription"]; ok {
		t.Fatalf("kind result retained undocumented subscription payload: %#v", compact)
	}
}
