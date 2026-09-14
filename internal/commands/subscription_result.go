package commands

import (
	"strconv"
	"strings"

	"github.com/Life-USTC/Bot/internal/lifedata"
)

// compactSubscriptionMutationData is the model-facing data for a subscription
// mutation. The API batch response may include the user's complete
// subscription, so only fields that describe the requested operation are
// retained here.
func compactSubscriptionMutationData(operation string, codes []string, semesterID int64, matches map[string]any) map[string]any {
	return map[string]any{
		"operation":   operation,
		"codes":       append([]string(nil), codes...),
		"semester_id": semesterID,
		"matches":     compactSubscriptionBatchResult(matches, codes),
	}
}

func compactSubscriptionBatchResult(matches map[string]any, requestedCodes []string) map[string]any {
	result := make(map[string]any)
	copySubscriptionBatchFields(result, matches)

	targets := subscriptionResultTargets(matches, requestedCodes)
	if _, ok := matches["sections"]; ok {
		result["sections"] = compactTargetSubscriptionSections(lifedata.MapSlice(matches["sections"]), targets)
	}
	if subscription, ok := matches["subscription"].(map[string]any); ok {
		compact := make(map[string]any)
		if _, hasSections := subscription["sections"]; hasSections {
			compact["sections"] = compactTargetSubscriptionSections(lifedata.MapSlice(subscription["sections"]), targets)
		}
		result["subscription"] = compact
	}
	return result
}

func copySubscriptionBatchFields(dst, src map[string]any) {
	if src == nil {
		return
	}
	if value, ok := src["action"]; ok && subscriptionResultScalar(value) {
		dst["action"] = value
	}
	for _, key := range []string{"total", "addedCount", "removedCount", "unchangedCount", "alreadySubscribedCount"} {
		if value, ok := subscriptionResultInt(src[key]); ok {
			dst[key] = value
		}
	}
	for _, key := range []string{"matchedCodes", "unmatchedCodes"} {
		if _, ok := src[key]; ok {
			dst[key] = lifedata.StringSlice(src[key])
		}
	}
	for _, key := range []string{"matchedSectionIds", "unmatchedSectionIds"} {
		if _, ok := src[key]; ok {
			dst[key] = subscriptionResultIDs(src[key])
		}
	}
	if suggestions, ok := src["suggestions"].(map[string]any); ok {
		compact := make(map[string]any, len(suggestions))
		for code, values := range suggestions {
			compact[code] = lifedata.StringSlice(values)
		}
		dst["suggestions"] = compact
	}
	if semester, ok := src["semester"].(map[string]any); ok {
		dst["semester"] = compactSubscriptionEntity(semester)
	}
}

func subscriptionResultInt(value any) (int, bool) {
	return lifedata.IntValue(value)
}

func subscriptionResultIDs(value any) []int {
	items := lifedata.AnySlice(value)
	if len(items) == 0 {
		if ids, ok := value.([]int); ok {
			return append([]int(nil), ids...)
		}
		return []int{}
	}
	ids := make([]int, 0, len(items))
	for _, item := range items {
		id, ok := lifedata.IntValue(item)
		if ok && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

type subscriptionResultTargetSet struct {
	codes map[string]struct{}
	ids   map[string]struct{}
	jwIDs map[string]struct{}
}

func subscriptionResultTargets(matches map[string]any, requestedCodes []string) subscriptionResultTargetSet {
	targets := subscriptionResultTargetSet{
		codes: make(map[string]struct{}),
		ids:   make(map[string]struct{}),
		jwIDs: make(map[string]struct{}),
	}
	for _, code := range requestedCodes {
		if code = subscriptionResultCode(code); code != "" {
			targets.codes[code] = struct{}{}
		}
	}

	matchedCodes := lifedata.StringSlice(matches["matchedCodes"])
	if len(matchedCodes) > 0 {
		targets.codes = make(map[string]struct{}, len(matchedCodes))
		for _, code := range matchedCodes {
			if code = subscriptionResultCode(code); code != "" {
				targets.codes[code] = struct{}{}
			}
		}
	}
	for _, code := range lifedata.StringSlice(matches["unmatchedCodes"]) {
		delete(targets.codes, subscriptionResultCode(code))
	}
	for _, id := range subscriptionResultIDs(matches["matchedSectionIds"]) {
		targets.ids[strconv.Itoa(id)] = struct{}{}
	}

	for _, section := range lifedata.MapSlice(matches["sections"]) {
		if !subscriptionResultSectionMatchesCode(section, targets.codes) {
			continue
		}
		if value := strings.TrimSpace(lifedata.FirstString(section, "id")); value != "" {
			targets.ids[value] = struct{}{}
		}
		if value := strings.TrimSpace(lifedata.FirstString(section, "jwId")); value != "" {
			targets.jwIDs[value] = struct{}{}
		}
	}
	return targets
}

func subscriptionResultSectionMatchesCode(section map[string]any, codes map[string]struct{}) bool {
	code := subscriptionResultCode(lifedata.FirstString(section, "code"))
	if code == "" {
		return false
	}
	_, ok := codes[code]
	return ok
}

func compactTargetSubscriptionSections(sections []map[string]any, targets subscriptionResultTargetSet) []map[string]any {
	result := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		if !subscriptionResultSectionMatches(section, targets) {
			continue
		}
		result = append(result, compactSubscriptionSection(section))
	}
	return result
}

func subscriptionResultSectionMatches(section map[string]any, targets subscriptionResultTargetSet) bool {
	if subscriptionResultSectionMatchesCode(section, targets.codes) {
		return true
	}
	if value := strings.TrimSpace(lifedata.FirstString(section, "id")); value != "" {
		if _, ok := targets.ids[value]; ok {
			return true
		}
	}
	if value := strings.TrimSpace(lifedata.FirstString(section, "jwId")); value != "" {
		if _, ok := targets.jwIDs[value]; ok {
			return true
		}
	}
	return false
}

func compactSubscriptionSection(section map[string]any) map[string]any {
	result := compactSubscriptionEntity(section)
	for _, key := range []string{"course", "semester", "teacher"} {
		if nested, ok := section[key].(map[string]any); ok {
			result[key] = compactSubscriptionEntity(nested)
		}
	}
	if _, ok := section["teachers"]; ok {
		teachers := make([]map[string]any, 0)
		for _, teacher := range lifedata.MapSlice(section["teachers"]) {
			teachers = append(teachers, compactSubscriptionEntity(teacher))
		}
		result["teachers"] = teachers
	}
	return result
}

func compactSubscriptionEntity(entity map[string]any) map[string]any {
	result := make(map[string]any)
	for _, key := range []string{
		"id", "jwId", "code", "name", "nameCn", "nameEn", "namePrimary", "nameSecondary", "kind",
	} {
		if value, ok := entity[key]; ok && value != nil {
			if subscriptionResultScalar(value) {
				result[key] = value
			}
		}
	}
	return result
}

func subscriptionResultScalar(value any) bool {
	switch value.(type) {
	case bool, float32, float64, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, string:
		return true
	default:
		return false
	}
}

func compactSubscriptionKindResult(result map[string]any, jwID int64) map[string]any {
	compact := make(map[string]any)
	if result == nil {
		return compact
	}
	for _, key := range []string{"sectionJwId", "kind"} {
		if value, ok := result[key]; ok && value != nil && subscriptionResultScalar(value) {
			compact[key] = value
		}
	}
	targets := subscriptionResultTargetSet{
		codes: make(map[string]struct{}),
		ids:   make(map[string]struct{}),
		jwIDs: map[string]struct{}{strconv.FormatInt(jwID, 10): {}},
	}
	if section, ok := result["section"].(map[string]any); ok {
		if subscriptionResultSectionMatches(section, targets) {
			compact["section"] = compactSubscriptionSection(section)
		}
	}
	if subscription, ok := result["subscription"].(map[string]any); ok {
		current := make(map[string]any)
		if _, hasSections := subscription["sections"]; hasSections {
			current["sections"] = compactTargetSubscriptionSections(lifedata.MapSlice(subscription["sections"]), targets)
		}
		compact["subscription"] = current
	}
	return compact
}

func subscriptionResultCode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
