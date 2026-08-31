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
		if descriptor.Exposure != ExposureModel && descriptor.Exposure != ExposureRedacted && descriptor.Exposure != ExposureHostOnly {
			t.Errorf("%s has invalid default exposure %q", descriptor.ID, descriptor.Exposure)
		}
		if descriptor.Requirements.Audience == "" {
			t.Errorf("%s has no audience requirement", descriptor.ID)
		}
		if descriptor.Help.Topic == "" || descriptor.Help.Title == "" || descriptor.Help.Summary == "" {
			t.Errorf("%s has incomplete help metadata: %#v", descriptor.ID, descriptor.Help)
		}

		invocation, ok := ParseInvocation(string(descriptor.ID))
		if !ok || invocation.Capability == nil || invocation.Capability.ID != descriptor.ID {
			t.Errorf("canonical form %q parsed as %#v, ok=%v", descriptor.ID, invocation, ok)
			continue
		}
		if got, want := invocation.Policy(), descriptor.PolicyFor(invocation); got != want {
			t.Errorf("%s invocation policy = %#v, descriptor policy = %#v", descriptor.ID, got, want)
		}
		if got := agentCommandNeedsConfirmation(invocation); got != (invocation.Policy().Confirmation == ConfirmUser) {
			t.Errorf("%s confirmation policy diverged", descriptor.ID)
		}
	}
	if len(seen) < 36 {
		t.Fatalf("registered %d capabilities, want all capability families", len(seen))
	}
}

func TestNestedSettingsMutationsResolveChildPolicy(t *testing.T) {
	tests := []struct {
		command string
		id      CapabilityID
	}{
		{command: "设置 通知 课表 开", id: CapabilityNotify},
		{command: "设置 工具调用 开", id: CapabilityAgentSettings},
	}
	for _, tt := range tests {
		invocation, ok := ParseInvocation(tt.command)
		if !ok || invocation.Capability == nil || invocation.Capability.ID != tt.id {
			t.Fatalf("%q parsed as %#v, ok=%v", tt.command, invocation, ok)
		}
		policy := invocation.Policy()
		if policy.Effect != EffectWrite || policy.Confirmation != ConfirmUser || policy.Audience != AudiencePrivate {
			t.Fatalf("%q policy = %#v", tt.command, policy)
		}
	}
}

func TestRemovedCommandFormsAreNotAccepted(t *testing.T) {
	for _, command := range []string{"课标", "代办", "todo待办", "profile", "setting", "sched", "zt", "js", "fb"} {
		if invocation, ok := ParseInvocation(command); ok {
			t.Errorf("obsolete form %q parsed as %#v", command, invocation)
		}
	}
}
