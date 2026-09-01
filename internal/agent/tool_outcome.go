package agent

import (
	"context"
	"strings"
	"sync"
)

type toolOutcomeContextKey struct{}

type toolOutcomeRegistry struct {
	errors sync.Map
}

func newToolOutcomeRegistry() *toolOutcomeRegistry { return &toolOutcomeRegistry{} }

func withToolOutcomes(ctx context.Context, outcomes *toolOutcomeRegistry) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolOutcomeContextKey{}, outcomes)
}

func toolOutcomesFromContext(ctx context.Context) *toolOutcomeRegistry {
	if ctx != nil {
		if outcomes, ok := ctx.Value(toolOutcomeContextKey{}).(*toolOutcomeRegistry); ok && outcomes != nil {
			return outcomes
		}
	}
	return &toolOutcomeRegistry{}
}

func (r *toolOutcomeRegistry) markError(callID string) {
	if r == nil {
		return
	}
	if callID = strings.TrimSpace(callID); callID != "" {
		r.errors.Store(callID, struct{}{})
	}
}

func (r *toolOutcomeRegistry) isError(callID string) bool {
	if r == nil {
		return false
	}
	_, found := r.errors.Load(strings.TrimSpace(callID))
	return found
}
