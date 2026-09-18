package commands

import (
	"testing"
)

func TestCapabilityDescriptorsDeclareCompleteContract(t *testing.T) {
	seen := map[CapabilityID]bool{}
	for _, descriptor := range CapabilityDescriptors() {
		if descriptor.ID == "" || seen[descriptor.ID] {
			t.Fatalf("invalid or duplicate capability id: %#v", descriptor.ID)
		}
		seen[descriptor.ID] = true
		if len(descriptor.Forms) == 0 {
			t.Errorf("%s has no command forms", descriptor.ID)
		}
		if descriptor.Input == nil || descriptor.Execute == nil || descriptor.Present == nil || descriptor.ResolvePolicy == nil {
			t.Errorf("%s has incomplete parser/executor/presenter policy", descriptor.ID)
		}
		if descriptor.Effect != EffectRead && descriptor.Effect != EffectWrite && descriptor.Effect != EffectDestructive {
			t.Errorf("%s has invalid default effect %q", descriptor.ID, descriptor.Effect)
		}
		if descriptor.Exposure != ExposureModel && descriptor.Exposure != ExposureHostOnly {
			t.Errorf("%s has invalid default exposure %q", descriptor.ID, descriptor.Exposure)
		}
		if descriptor.Requirements.DataScope == "" {
			t.Errorf("%s has no audience requirement", descriptor.ID)
		}
		if descriptor.Help.Topic == "" || descriptor.Help.Title == "" || descriptor.Help.Summary == "" {
			t.Errorf("%s has incomplete help metadata: %#v", descriptor.ID, descriptor.Help)
		}

		invocation, ok := NewInvocation(descriptor.ID, nil)
		if !ok {
			for _, example := range descriptor.Help.Examples {
				if example.Capability == descriptor.ID {
					invocation, ok = example.Invocation()
					break
				}
			}
		}
		if !ok || invocation.Capability == nil || invocation.Capability.ID != descriptor.ID {
			t.Errorf("capability %q has no valid invocation example: %#v, ok=%v", descriptor.ID, invocation, ok)
			continue
		}
		if got, want := invocation.Policy(), descriptor.PolicyFor(invocation); got != want {
			t.Errorf("%s invocation policy = %#v, descriptor policy = %#v", descriptor.ID, got, want)
		}
	}
	if len(seen) < 35 {
		t.Fatalf("registered %d capabilities, want all capability families", len(seen))
	}
}

func TestTopLevelNotificationMutationsResolvePolicy(t *testing.T) {
	for _, command := range []string{"通知 课表 开", "通知 待办 开"} {
		invocation, ok := ParseInvocation(command)
		if !ok || invocation.Capability == nil || invocation.ID() != CapabilityNotify {
			t.Fatalf("notification invocation=%#v ok=%v", invocation, ok)
		}
		policy := invocation.Policy()
		if policy.Effect != EffectWrite || policy.DataScope != DataScopeUserPrivate {
			t.Fatalf("notification policy = %#v", policy)
		}
	}
	if _, accepted := ParseInvocation("设置 通知 课表 开"); accepted {
		t.Fatal("retired nested settings command was accepted")
	}
}

func TestLoginPolicyDistinguishesReadFromStartingLogin(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		effect CapabilityEffect
	}{
		{name: "start login", effect: EffectWrite},
		{name: "show login status", args: []string{"status"}, effect: EffectRead},
		{name: "show login help", args: []string{"help"}, effect: EffectRead},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation, ok := NewInvocation(CapabilityLogin, test.args)
			if !ok {
				t.Fatalf("login invocation with args %#v is invalid", test.args)
			}
			policy := invocation.Policy()
			if policy.Effect != test.effect || policy.DataScope != DataScopeUserPrivate || policy.Exposure != ExposureHostOnly {
				t.Fatalf("login policy = %#v", policy)
			}
		})
	}
}

func TestCapabilityDataScopesSeparatePublicAndUserPrivateReads(t *testing.T) {
	tests := []struct {
		command string
		want    DataScope
	}{
		{command: "校车 西区 高新区", want: DataScopePublic},
		{command: "校车 偏好", want: DataScopeUserPrivate},
		{command: "通知", want: DataScopeUserPrivate},
		{command: "课程 数学分析", want: DataScopePublic},
		{command: "课表", want: DataScopeUserPrivate},
		{command: "订阅 链接", want: DataScopeUserPrivate},
	}
	for _, test := range tests {
		invocation, ok := ParseInvocation(test.command)
		if !ok {
			t.Fatalf("%q did not parse", test.command)
		}
		if got := invocation.Policy().DataScope; got != test.want {
			t.Errorf("%q data scope = %q, want %q", test.command, got, test.want)
		}
	}
}
