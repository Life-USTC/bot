package commands

import "github.com/Life-USTC/Bot/internal/lifedata"

// compactSubscriptionMutationData is the model-facing data for a subscription
// mutation. The API batch response may include the user's complete
// subscription, so only fields that describe the requested operation are
// retained here.
func compactSubscriptionMutationData(operation string, codes []string, semesterID int64, matches map[string]any) map[string]any {
	return map[string]any{
		"operation":   operation,
		"codes":       append([]string(nil), codes...),
		"semester_id": semesterID,
		"matches":     compactSubscriptionBatchResult(matches),
	}
}

func compactSubscriptionBatchResult(matches map[string]any) map[string]any {
	result := make(map[string]any)
	copySubscriptionBatchFields(result, matches)

	matchedSections := lifedata.MapSlice(matches["sections"])
	if _, ok := matches["sections"]; ok {
		// sections is already resolved by the API for this operation. Keep every
		// returned target, including multiple sections expanded from one course
		// code, while projecting away redundant metadata.
		result["sections"] = compactSubscriptionSections(matchedSections)
	}
	if subscription, ok := matches["subscription"].(map[string]any); ok {
		compact := make(map[string]any)
		if _, hasSections := subscription["sections"]; hasSections {
			compact["sections"] = compactSubscriptionSectionsByID(
				lifedata.MapSlice(subscription["sections"]),
				subscriptionResultSectionIDs(matchedSections),
			)
		}
		result["subscription"] = compact
	}
	return result
}

func copySubscriptionBatchFields(dst, src map[string]any) {
	if src == nil {
		return
	}
	for _, key := range []string{
		"action", "total", "addedCount", "removedCount", "unchangedCount", "alreadySubscribedCount",
		"matchedCodes", "unmatchedCodes", "matchedSectionIds", "unmatchedSectionIds", "suggestions",
	} {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
	if semester, ok := src["semester"].(map[string]any); ok {
		dst["semester"] = compactSubscriptionEntity(semester)
	}
}

func subscriptionResultSectionIDs(sections []map[string]any) map[string]struct{} {
	ids := make(map[string]struct{}, len(sections))
	for _, section := range sections {
		if id := lifedata.FirstString(section, "id"); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func compactSubscriptionSections(sections []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		result = append(result, compactSubscriptionSection(section))
	}
	return result
}

func compactSubscriptionSectionsByID(sections []map[string]any, ids map[string]struct{}) []map[string]any {
	result := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		if _, ok := ids[lifedata.FirstString(section, "id")]; !ok {
			continue
		}
		result = append(result, compactSubscriptionSection(section))
	}
	return result
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
		if value, ok := entity[key]; ok {
			result[key] = value
		}
	}
	return result
}

func compactSubscriptionKindResult(result map[string]any) map[string]any {
	compact := make(map[string]any)
	for _, key := range []string{"sectionJwId", "kind"} {
		if value, ok := result[key]; ok {
			compact[key] = value
		}
	}
	return compact
}
