package auth

import (
	"context"

	"github.com/Life-USTC/Bot/internal/store"
)

func WithRefresh[T any](ctx context.Context, manager *Manager, ident store.Identity, token string, fetch func(string) (T, error)) (T, error) {
	data, err := fetch(token)
	if manager == nil {
		return data, err
	}
	if refreshed, ok := manager.RefreshIfUnauthorized(ctx, ident, err); ok {
		return fetch(refreshed)
	}
	return data, err
}
