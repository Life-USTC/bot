package commands

import (
	"reflect"
	"testing"
)

func TestClassNameCanPrecedeScheduleAction(t *testing.T) {
	invocation, ok := ParseInvocation("课堂 数学分析 课表")
	if !ok {
		t.Fatal("课堂 name schedule query should be accepted")
	}
	if invocation.ID() != CapabilitySectionSchedules {
		t.Fatalf("capability = %s, want %s", invocation.ID(), CapabilitySectionSchedules)
	}
	if !reflect.DeepEqual(invocation.Args, []string{"数学分析"}) {
		t.Fatalf("args = %#v, want %#v", invocation.Args, []string{"数学分析"})
	}
}

func TestAcademicExplicitActionsPreserveSearchTerms(t *testing.T) {
	for _, tc := range []struct{ text, name, target string }{
		{"课程 搜索 课表", "course_search", "课表"},
		{"课堂 搜索 作业", "section_search", "作业"},
		{"课程 查看 考试", "course_by_jw_id", "考试"},
		{"课堂 查看 课表", "section_by_jw_id", "课表"},
	} {
		inv, ok := ParseInvocation(tc.text)
		if !ok || inv.Name != tc.name || !reflect.DeepEqual(inv.Args, []string{tc.target}) {
			t.Fatalf("%s -> %#v, %v", tc.text, inv, ok)
		}
	}
}
