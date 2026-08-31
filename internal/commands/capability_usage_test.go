package commands

import (
	"reflect"
	"strings"
	"testing"
)

func TestCapabilityUsageExamplesAreStructuredAndInvokable(t *testing.T) {
	for _, usage := range CapabilityUsages() {
		for _, example := range append(append([]CapabilityUsageExample{}, usage.Examples...), usage.Shortcuts...) {
			if example.Capability == "" {
				t.Errorf("%s example %q has no capability", usage.ID, example.Command)
			}
			if example.Arguments == nil {
				t.Errorf("%s example %q has nil arguments", usage.ID, example.Command)
			}
			invocation, ok := example.Invocation()
			if !ok {
				t.Errorf("%s example %q has invalid invocation: capability=%q args=%#v", usage.ID, example.Command, example.Capability, example.Arguments)
				continue
			}
			if invocation.ID() != example.Capability {
				t.Errorf("%s example %q resolves to %s, want %s", usage.ID, example.Command, invocation.ID(), example.Capability)
			}
		}
	}
}

func TestCapabilityUsageIsDerivedFromDescriptor(t *testing.T) {
	for _, descriptor := range CapabilityDescriptors() {
		usage, ok := CapabilityUsageFor(descriptor.ID)
		if !ok {
			t.Fatalf("usage missing for %s", descriptor.ID)
		}
		if usage.ID != descriptor.ID || usage.Topic != descriptor.Help.Topic || usage.Title != descriptor.Help.Title || usage.Summary != descriptor.Help.Summary {
			t.Errorf("usage for %s diverges from descriptor: %#v", descriptor.ID, usage)
		}
		if got := usage.UsageHint(); len(descriptor.Help.Examples) > 0 && got == "" {
			t.Errorf("usage for %s has examples but no usage hint", descriptor.ID)
		}
	}
}

func TestPlaceholderUsageExamplesCarryStructuredArguments(t *testing.T) {
	want := map[string]struct {
		capability CapabilityID
		arguments  []string
	}{
		"反馈 <你的建议>":                    {CapabilityFeedback, []string{"<你的建议>"}},
		"课程 搜索 培养层次ID <ID>":            {CapabilityCourseSearch, []string{"education_level_id", "<ID>"}},
		"课程 搜索 类别ID <ID>":              {CapabilityCourseSearch, []string{"category_id", "<ID>"}},
		"课程 查看 <JW ID>":                {CapabilityCourseByJWID, []string{"<JW ID>"}},
		"教学班 查看 <JW ID>":               {CapabilitySectionByJWID, []string{"<JW ID>"}},
		"老师 查看 <ID>":                   {CapabilityTeacherByID, []string{"<ID>"}},
		"订阅 删除 <JW ID>":                {CapabilityUnsubscribeSectionByJWID, []string{"<JW ID>"}},
		"教学班 课表 <JW ID> <开始日期> <结束日期>": {CapabilitySectionSchedules, []string{"<JW ID>", "<开始日期>", "<结束日期>"}},
		"教学班 考试 <JW ID>":               {CapabilitySectionExams, []string{"<JW ID>"}},
		"教学班 作业 <JW ID>":               {CapabilitySectionHomeworks, []string{"<JW ID>"}},
	}
	for _, usage := range CapabilityUsages() {
		for _, example := range usage.Examples {
			entry, ok := want[example.Command]
			if !ok {
				continue
			}
			if example.Capability != entry.capability || !reflect.DeepEqual(example.Arguments, entry.arguments) {
				t.Errorf("example %q = capability %q args %#v, want %q %#v", example.Command, example.Capability, example.Arguments, entry.capability, entry.arguments)
			}
			delete(want, example.Command)
		}
	}
	if len(want) != 0 {
		t.Fatalf("placeholder examples missing from usage contract: %#v", want)
	}
}

func TestHelpTopicRowsDeduplicateExamples(t *testing.T) {
	for _, section := range helpDetailSections() {
		seen := map[string]bool{}
		for _, row := range section.rows {
			if row.topic == "shortcuts" {
				continue
			}
			key := row.topic + "\x00" + strings.TrimSpace(row.command)
			if seen[key] {
				t.Errorf("topic %q repeats command %q", row.topic, row.command)
			}
			seen[key] = true
		}
	}
}

func TestCapabilityIDsResolveTheirDescriptorTopics(t *testing.T) {
	for _, descriptor := range CapabilityDescriptors() {
		if descriptor.ID == CapabilityHelp {
			continue
		}
		if got := capabilityTopic(string(descriptor.ID)); got != descriptor.Help.Topic {
			t.Errorf("capability %s topic = %q, want %q", descriptor.ID, got, descriptor.Help.Topic)
		}
	}
}

func TestCapabilityUsageCopiesArguments(t *testing.T) {
	usage, ok := CapabilityUsageFor(CapabilityTodo)
	if !ok || len(usage.Examples) == 0 {
		t.Fatal("todo usage examples missing")
	}
	index := -1
	for i, example := range usage.Examples {
		if len(example.Arguments) > 0 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("todo should have an example with structured arguments")
	}
	original := usage.Examples[index].Arguments
	original[0] = "mutated"
	again, ok := CapabilityUsageFor(CapabilityTodo)
	if !ok || again.Examples[index].Arguments[0] == "mutated" {
		t.Fatal("usage arguments were not copied")
	}
}
