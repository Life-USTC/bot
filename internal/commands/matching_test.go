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
