package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Life-USTC/Bot/internal/responses"
	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/toolresult"
)

// CommandImageStore retains immutable render inputs, independently of delivery.
type CommandImageStore interface {
	SaveCommandImage(context.Context, store.Identity, string, *responses.Image) (string, error)
}

// RegisterResponseImages assigns references in presentation order. The key
// identifies the already completed command, so retries reuse the same images.
func RegisterResponseImages(ctx context.Context, images CommandImageStore, ident store.Identity, key string, response *Response) error {
	response.Images = nil
	response.Parts = slices.Clone(response.Parts)
	if response.Image != nil {
		payload, err := json.Marshal(response.Image)
		if err != nil {
			return err
		}
		// A recovered read may observe new data before its outcome is committed.
		// Never pair that new result with an older immutable image under the same execution key.
		imageKey := fmt.Sprintf("%s:image:%x", key, sha256.Sum256(payload))
		id, err := images.SaveCommandImage(ctx, ident, imageKey, response.Image)
		if err != nil {
			return err
		}
		response.Images = append(response.Images, toolresult.ImageReference{ID: id, Title: response.Image.Title})
	}
	for i := range response.Parts {
		if err := RegisterResponseImages(ctx, images, ident, fmt.Sprintf("%s:part:%d", key, i), &response.Parts[i]); err != nil {
			return err
		}
		response.Images = append(response.Images, response.Parts[i].Images...)
	}
	return nil
}
