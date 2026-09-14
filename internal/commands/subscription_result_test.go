package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactSubscriptionBatchResultKeepsOnlyMutationTargets(t *testing.T) {
	const unrelatedMarker = "unrelated-course-metadata"
	matches := map[string]any{
		"action":                 "add",
		"semester":               map[string]any{"id": float64(1), "jwId": float64(202601), "nameCn": "2026年春季学期", "extra": unrelatedMarker},
		"matchedCodes":           []any{"TARGET1001.01"},
		"unmatchedCodes":         []any{"MISSING1001.01"},
		"matchedSectionIds":      []any{float64(101)},
		"addedCount":             float64(1),
		"alreadySubscribedCount": float64(0),
		"sections": []any{
			map[string]any{
				"id": float64(101), "jwId": float64(10001), "code": "TARGET1001.01",
				"course": map[string]any{
					"id": float64(11), "jwId": float64(11001), "code": "TARGET1001", "namePrimary": "目标课程",
					"metadata": strings.Repeat(unrelatedMarker, 100),
				},
				"semester": map[string]any{"id": float64(1), "jwId": float64(202601), "nameCn": "2026年春季学期"},
				"teachers": []any{map[string]any{"id": float64(21), "jwId": float64(21001), "code": "T001", "namePrimary": "目标老师"}},
			},
			map[string]any{
				"id": float64(999), "jwId": float64(99999), "code": "OTHER1001.01",
				"course": map[string]any{"namePrimary": unrelatedMarker},
			},
		},
		"subscription": map[string]any{
			"userId": "private-user",
			"sections": []any{
				map[string]any{"id": float64(101), "jwId": float64(10001), "code": "TARGET1001.01", "kind": "regular"},
				map[string]any{"id": float64(999), "jwId": float64(99999), "code": "OTHER1001.01", "kind": "auditor", "course": map[string]any{"namePrimary": unrelatedMarker}},
			},
		},
	}

	data := compactSubscriptionMutationData("subscribe", []string{"TARGET1001.01", "MISSING1001.01"}, 0, matches)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), unrelatedMarker) || strings.Contains(string(encoded), "private-user") {
		t.Fatalf("compact mutation data leaked unrelated response data: %s", encoded)
	}

	compact, ok := data["matches"].(map[string]any)
	if !ok {
		t.Fatalf("matches = %#v", data["matches"])
	}
	if compact["action"] != "add" || compact["addedCount"] != 1 || compact["alreadySubscribedCount"] != 0 {
		t.Fatalf("operation fields = %#v", compact)
	}
	sections, ok := compact["sections"].([]map[string]any)
	if !ok || len(sections) != 1 {
		t.Fatalf("target sections = %#v", compact["sections"])
	}
	section := sections[0]
	if section["id"] != float64(101) || section["jwId"] != float64(10001) || section["code"] != "TARGET1001.01" {
		t.Fatalf("target identity = %#v", section)
	}
	if course, ok := section["course"].(map[string]any); !ok || course["jwId"] != float64(11001) || course["namePrimary"] != "目标课程" {
		t.Fatalf("target course = %#v", section["course"])
	}
	if semester, ok := section["semester"].(map[string]any); !ok || semester["jwId"] != float64(202601) {
		t.Fatalf("target semester = %#v", section["semester"])
	}
	if teachers, ok := section["teachers"].([]map[string]any); !ok || len(teachers) != 1 || teachers[0]["jwId"] != float64(21001) {
		t.Fatalf("target teachers = %#v", section["teachers"])
	}
	current, ok := compact["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("current subscription = %#v", compact["subscription"])
	}
	currentSections, ok := current["sections"].([]map[string]any)
	if !ok || len(currentSections) != 1 || currentSections[0]["kind"] != "regular" {
		t.Fatalf("current target membership = %#v", current["sections"])
	}
}

func TestCompactSubscriptionKindResultKeepsTargetMembershipOnly(t *testing.T) {
	const unrelatedMarker = "unrelated-kind-metadata"
	result := map[string]any{
		"sectionJwId": float64(10001),
		"kind":        "teaching_assistant",
		"subscription": map[string]any{
			"sections": []any{
				map[string]any{"id": float64(101), "jwId": float64(10001), "code": "TARGET1001.01", "kind": "teaching_assistant"},
				map[string]any{"id": float64(999), "jwId": float64(99999), "code": "OTHER1001.01", "course": map[string]any{"namePrimary": unrelatedMarker}},
			},
			"metadata": unrelatedMarker,
		},
		"metadata": strings.Repeat(unrelatedMarker, 100),
	}

	compact := compactSubscriptionKindResult(result, 10001)
	encoded, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), unrelatedMarker) {
		t.Fatalf("compact kind result leaked unrelated response data: %s", encoded)
	}
	if compact["sectionJwId"] != float64(10001) || compact["kind"] != "teaching_assistant" {
		t.Fatalf("kind result = %#v", compact)
	}
	current, ok := compact["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("current subscription = %#v", compact["subscription"])
	}
	sections, ok := current["sections"].([]map[string]any)
	if !ok || len(sections) != 1 || sections[0]["jwId"] != float64(10001) {
		t.Fatalf("current target membership = %#v", current["sections"])
	}
}
