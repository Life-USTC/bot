package commands

// The current-subscription endpoint answers with the complete jw record of
// every subscribed section: dozens of scheduling scalars plus nested course,
// category, campus, department and teacher objects. Read capabilities use only
// a handful of those fields, so each one projects the response here instead of
// forwarding the bulk record to the model.

import "github.com/Life-USTC/Bot/internal/lifedata"

// compactCurrentSubscription is the model-facing view of a current
// subscription for capabilities that list the subscribed sections. The private
// calendar URL is deliberately left out: only the capability that answers with
// that link may carry the credential.
func compactCurrentSubscription(subscription map[string]any) map[string]any {
	return map[string]any{
		"sections": compactSubscriptionSections(lifedata.SubscriptionSections(subscription)),
	}
}

// compactSubscriptionExams is the model-facing view of the exams carried by a
// current subscription. Only the exam and the identity of the section it
// belongs to are retained; the rest of the section record is dropped.
func compactSubscriptionExams(exams []subscriptionExam) []map[string]any {
	result := make([]map[string]any, 0, len(exams))
	for _, item := range exams {
		result = append(result, compactSubscriptionExam(item))
	}
	return result
}

func compactSubscriptionExam(item subscriptionExam) map[string]any {
	compact := make(map[string]any)
	for _, key := range []string{
		"id", "jwId", "examDate", "date", "startTime", "endTime", "examMode",
	} {
		if value, ok := item.exam[key]; ok {
			compact[key] = value
		}
	}
	if _, ok := item.exam["examRooms"]; ok {
		compact["examRooms"] = compactExamRooms(lifedata.MapSlice(item.exam["examRooms"]))
	}
	compact["section"] = compactSubscriptionExamSection(item.section)
	return compact
}

// compactSubscriptionExamSection keeps the identity an exam line prints and
// the model may cite. Teachers are not part of an exam answer, so the section
// projection here is narrower than compactSubscriptionSection.
func compactSubscriptionExamSection(section map[string]any) map[string]any {
	result := compactSubscriptionEntity(section)
	for _, key := range []string{"course", "semester"} {
		if nested, ok := section[key].(map[string]any); ok {
			result[key] = compactSubscriptionEntity(nested)
		}
	}
	return result
}

func compactExamRooms(rooms []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(rooms))
	for _, room := range rooms {
		compact := compactSubscriptionEntity(room)
		if value, ok := room["room"]; ok {
			compact["room"] = value
		}
		result = append(result, compact)
	}
	return result
}
