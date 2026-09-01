package commands

import "strings"

// CapabilityUsageExample is the shared representation of one documented
// command example. Command is the user-facing form; Capability and Arguments
// are the normalized form an integration can invoke directly. Keeping both in
// one value prevents Agent integrations from having to parse help text.
type CapabilityUsageExample struct {
	Command      string             `json:"command"`
	Description  string             `json:"description"`
	Capability   CapabilityID       `json:"capability"`
	Arguments    []string           `json:"arguments"`
	Effect       CapabilityEffect   `json:"effect,omitempty"`
	Confirmation ConfirmationPolicy `json:"confirmation,omitempty"`
	DataScope    DataScope          `json:"dataScope,omitempty"`
}

// HelpExample is kept as the name used by HelpMetadata while sharing the
// structured usage contract with non-help consumers.
type HelpExample = CapabilityUsageExample

// CapabilityUsage is the read-only, integration-facing view of a descriptor's
// command usage. It is derived from CapabilityDescriptor; it is not a second
// command registry.
type CapabilityUsage struct {
	ID           CapabilityID             `json:"id"`
	Group        string                   `json:"group"`
	Summary      string                   `json:"summary"`
	Forms        []string                 `json:"forms"`
	Effect       CapabilityEffect         `json:"effect"`
	Confirmation ConfirmationPolicy       `json:"confirmation"`
	DataScope    DataScope                `json:"dataScope"`
	Exposure     ResultExposure           `json:"exposure"`
	Examples     []CapabilityUsageExample `json:"examples"`
}

// Invocation returns the validated structured invocation represented by an
// example. Examples are bound during descriptor initialization, so callers do
// not need to parse the human-readable Command field.
func (e CapabilityUsageExample) Invocation() (Invocation, bool) {
	if e.Capability == "" {
		return Invocation{}, false
	}
	return NewInvocation(e.Capability, e.Arguments)
}

// Usage returns the descriptor's structured usage contract. Slices are copied
// so callers cannot mutate the registry through the returned value.
func (d CapabilityDescriptor) Usage() CapabilityUsage {
	invocation := descriptorDefaultInvocation(d)
	policy := d.PolicyFor(invocation)
	return CapabilityUsage{
		ID:           d.ID,
		Group:        d.Help.Topic,
		Summary:      d.Help.Summary,
		Forms:        append([]string(nil), d.Forms...),
		Effect:       policy.Effect,
		Confirmation: policy.Confirmation,
		DataScope:    policy.DataScope,
		Exposure:     policy.Exposure,
		Examples:     copyUsageExamples(d.Help.Examples),
	}
}

// CapabilityUsages returns the usage contract for every registered
// capability. The order is the descriptor order used by the command registry.
func CapabilityUsages() []CapabilityUsage {
	result := make([]CapabilityUsage, 0, len(capabilityDescriptors))
	for _, descriptor := range capabilityDescriptors {
		result = append(result, descriptor.Usage())
	}
	return result
}

// CapabilityUsageFor returns one capability's usage contract by stable ID.
func CapabilityUsageFor(id CapabilityID) (CapabilityUsage, bool) {
	descriptor, ok := descriptorForID(string(id))
	if !ok {
		return CapabilityUsage{}, false
	}
	return descriptor.Usage(), true
}

// CapabilityUsageExamples returns executable suggestions for invalid input.
func CapabilityUsageExamples(id CapabilityID) []CapabilityUsageExample {
	usage, ok := CapabilityUsageFor(id)
	if !ok {
		return nil
	}
	return usage.Examples
}

func copyUsageExamples(examples []CapabilityUsageExample) []CapabilityUsageExample {
	if examples == nil {
		return nil
	}
	result := make([]CapabilityUsageExample, len(examples))
	for i, example := range examples {
		result[i] = example
		if example.Arguments != nil {
			result[i].Arguments = append([]string{}, example.Arguments...)
		}
	}
	return result
}

func descriptorDefaultInvocation(descriptor CapabilityDescriptor) Invocation {
	if invocation, ok := NewInvocation(descriptor.ID, nil); ok {
		return invocation
	}
	for _, usageExample := range descriptor.Help.Examples {
		if invocation, ok := usageExample.Invocation(); ok {
			return invocation
		}
	}
	return Invocation{Capability: &descriptor, Name: string(descriptor.ID)}
}

// bindCapabilityUsage fills the structured half of each documented example
// from the final command registry. Human examples remain the source shown to
// users, while integrations receive the already normalized capability and
// arguments. Parenthetical shortcut labels are presentation-only.
func bindCapabilityUsage(descriptors []CapabilityDescriptor) {
	for i := range descriptors {
		bindUsageExamples(descriptors[i].ID, descriptors[i].Help.Examples)
		bindUsageExamples(descriptors[i].ID, descriptors[i].Help.Shortcuts)
	}
}

func bindUsageExamples(owner CapabilityID, examples []CapabilityUsageExample) {
	for i := range examples {
		example := &examples[i]
		if example.Capability != "" || example.Arguments != nil {
			if example.Capability == "" || !usageExampleInvocationIsValid(*example) {
				panic("invalid structured usage example for " + string(owner) + ": " + example.Command)
			}
			bindUsageExamplePolicy(example)
			continue
		}
		command := usageCommandWithoutShortcutLabel(example.Command)
		invocation, ok := ParseInvocation(command)
		if !ok || invocation.Capability == nil {
			panic("unable to bind usage example for " + string(owner) + ": " + example.Command)
		}
		example.Capability = invocation.ID()
		example.Arguments = append([]string{}, invocation.Args...)
		bindUsageExamplePolicy(example)
	}
}

func bindUsageExamplePolicy(example *CapabilityUsageExample) {
	invocation, ok := example.Invocation()
	if !ok {
		return
	}
	policy := invocation.Policy()
	example.Effect = policy.Effect
	example.Confirmation = policy.Confirmation
	example.DataScope = policy.DataScope
}

func usageExampleInvocationIsValid(example CapabilityUsageExample) bool {
	_, ok := NewInvocation(example.Capability, example.Arguments)
	return ok
}

func usageCommandWithoutShortcutLabel(command string) string {
	command = strings.TrimSpace(command)
	if start := strings.Index(command, "（"); start >= 0 && strings.HasSuffix(command, "）") {
		return strings.TrimSpace(command[:start])
	}
	return command
}
