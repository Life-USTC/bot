package agent

import (
	"context"
	"strings"
	"sync"
)

type toolOutcomeContextKey struct{}

type toolOutcomeRegistry struct {
	errors sync.Map
	// media records that the host delivered a rendered attachment for a tool
	// call. A card is sent by the host rather than written by the model, so
	// without this the model would answer as if the user had only seen text.
	media sync.Map
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

func (r *toolOutcomeRegistry) markDeliveredMedia(callID string) {
	if r == nil {
		return
	}
	if callID = strings.TrimSpace(callID); callID != "" {
		r.media.Store(callID, struct{}{})
	}
}

func (r *toolOutcomeRegistry) deliveredMedia(callID string) bool {
	if r == nil {
		return false
	}
	_, found := r.media.Load(strings.TrimSpace(callID))
	return found
}
