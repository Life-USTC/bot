package life

import (
	"context"
	"fmt"

	"github.com/Life-USTC/Bot/internal/openapi"
)

func (c *Client) SetSubscriptionKind(ctx context.Context, token string, jwID int64, kind string) (map[string]any, error) {
	value := openapi.SubscriptionKindUpdateRequestSchemaKind(kind)
	if jwID <= 0 || !value.Valid() {
		return nil, fmt.Errorf("a positive section JW ID and valid subscription kind are required")
	}
	var out map[string]any
	resp, err := c.Typed(ctx, token).PatchApiWorkspaceSubscriptionsJwId(ctx, jwID, openapi.SubscriptionKindUpdateRequestSchema{Kind: value})
	err = typedJSON(resp, err, "update subscription kind", &out)
	return out, err
}
