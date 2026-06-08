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

func WithRefreshVoid(ctx context.Context, manager *Manager, ident store.Identity, token string, fetch func(string) error) error {
	_, err := WithRefresh(ctx, manager, ident, token, func(token string) (struct{}, error) {
		return struct{}{}, fetch(token)
	})
	return err
}
