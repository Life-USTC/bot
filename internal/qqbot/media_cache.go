package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Life-USTC/Bot/internal/store"
	"github.com/Life-USTC/Bot/internal/textutil"
)

const qqMediaCacheLimit = 512

type qqMediaCache struct {
	mu      sync.Mutex
	items   map[string]qqMediaCacheEntry
	uploads singleflight.Group
}

type qqMediaCacheEntry struct {
	fileInfo  json.RawMessage
	expiresAt time.Time
}

type qqMediaLookup struct {
	entry    qqMediaCacheEntry
	hit      bool
	uploader *byte
}

func (b *Bot) sendCachedRichMedia(
	ctx context.Context,
	ident store.Identity,
	imageURL, msgID, eventID string,
	msgSeq int,
) (store.MessageAcceptance, error) {
	return b.sendCachedRichMediaContent(ctx, ident, imageURL, "", msgID, eventID, msgSeq)
}

func (b *Bot) sendCachedRichMediaContent(
	ctx context.Context,
	ident store.Identity,
	imageURL, content, msgID, eventID string,
	msgSeq int,
) (store.MessageAcceptance, error) {
	key, err := qqMediaCacheKey(ident, imageURL)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	lookup, err := b.cachedOrUploadRichMedia(ctx, ident, key, imageURL)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	startedAt := time.Now()
	receipt, err := b.sendRichMediaContentTo(ctx, ident, lookup.entry.fileInfo, content, msgID, eventID, msgSeq)
	if err == nil {
		if lookup.hit {
			receipt.DeliveryMethod = store.DeliveryMethodMediaCache
		} else {
			receipt.DeliveryMethod = store.DeliveryMethodMediaUpload
		}
		b.logf("QQ bot media sent: conversation_type=%q delivery_method=%q send_ms=%d",
			ident.ConversationType, receipt.DeliveryMethod, time.Since(startedAt).Milliseconds())
		return receipt, nil
	}
	if !lookup.hit || isUncertainSendError(err) || !isQQMediaCacheRejection(err) {
		return store.MessageAcceptance{}, err
	}

	b.mediaCache.remove(key)
	b.logf("QQ bot media cache entry rejected; reuploading: conversation_type=%q", ident.ConversationType)
	refreshed, err := b.uploadAndCacheRichMedia(ctx, ident, key, imageURL)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	startedAt = time.Now()
	receipt, err = b.sendRichMediaContentTo(ctx, ident, refreshed.fileInfo, content, msgID, eventID, msgSeq)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	receipt.DeliveryMethod = store.DeliveryMethodMediaUpload
	b.logf("QQ bot media sent after refresh: conversation_type=%q delivery_method=%q send_ms=%d",
		ident.ConversationType, receipt.DeliveryMethod, time.Since(startedAt).Milliseconds())
	return receipt, nil
}

func isQQMediaCacheRejection(err error) bool {
	var statusErr qqBotHTTPStatusError
	return errors.As(err, &statusErr) && isPermanentHTTPStatus(statusErr.status)
}

func (b *Bot) cachedOrUploadRichMedia(
	ctx context.Context,
	ident store.Identity,
	key, imageURL string,
) (qqMediaLookup, error) {
	if entry, ok := b.mediaCache.get(key, b.mediaNow()); ok {
		b.logf("QQ bot media cache hit: conversation_type=%q", ident.ConversationType)
		return qqMediaLookup{entry: entry, hit: true}, nil
	}

	caller := new(byte)
	value, err, _ := b.mediaCache.uploads.Do(key, func() (any, error) {
		if entry, ok := b.mediaCache.get(key, b.mediaNow()); ok {
			return qqMediaLookup{entry: entry, hit: true}, nil
		}
		entry, err := b.uploadAndCacheRichMedia(ctx, ident, key, imageURL)
		if err != nil {
			return nil, err
		}
		return qqMediaLookup{entry: entry, uploader: caller}, nil
	})
	if err != nil {
		return qqMediaLookup{}, err
	}
	lookup := value.(qqMediaLookup)
	if lookup.uploader != nil && lookup.uploader != caller {
		lookup.hit = true
	}
	if lookup.hit {
		b.logf("QQ bot media cache shared upload: conversation_type=%q", ident.ConversationType)
	} else {
		b.logf("QQ bot media cache miss: conversation_type=%q", ident.ConversationType)
	}
	return lookup, nil
}

func (b *Bot) uploadAndCacheRichMedia(
	ctx context.Context,
	ident store.Identity,
	key, imageURL string,
) (qqMediaCacheEntry, error) {
	uploaded, err := b.uploadRichMedia(ctx, ident, imageURL)
	if err != nil {
		return qqMediaCacheEntry{}, err
	}
	entry := qqMediaCacheEntry{
		fileInfo:  cloneRawMessage(uploaded.FileInfo),
		expiresAt: qqMediaExpiresAt(b.mediaNow(), uploaded.TTL),
	}
	b.mediaCache.put(key, entry, b.mediaNow())
	return entry, nil
}

func qqMediaCacheKey(ident store.Identity, imageURL string) (string, error) {
	scene := textutil.LowerTrim(ident.ConversationType)
	switch scene {
	case "", "private":
		scene = "private"
	case "group":
	default:
		return "", errors.New("qq bot media cache only supports group and private conversations")
	}
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", errors.New("qq bot media URL is empty")
	}
	return scene + "\x00" + imageURL, nil
}

func qqMediaExpiresAt(now time.Time, ttl uint) time.Time {
	if ttl == 0 {
		return time.Time{}
	}
	duration := time.Duration(ttl) * time.Second
	margin := duration / 10
	if margin > 30*time.Second {
		margin = 30 * time.Second
	}
	return now.Add(duration - margin)
}

func (b *Bot) mediaNow() time.Time {
	if b.now != nil {
		return b.now().UTC()
	}
	return time.Now().UTC()
}

func (c *qqMediaCache) get(key string, now time.Time) (qqMediaCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return qqMediaCacheEntry{}, false
	}
	if entry.expiresAt.IsZero() || !now.Before(entry.expiresAt) {
		delete(c.items, key)
		return qqMediaCacheEntry{}, false
	}
	entry.fileInfo = cloneRawMessage(entry.fileInfo)
	return entry, true
}

func (c *qqMediaCache) put(key string, entry qqMediaCacheEntry, now time.Time) {
	if entry.expiresAt.IsZero() || !now.Before(entry.expiresAt) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]qqMediaCacheEntry{}
	}
	for itemKey, item := range c.items {
		if !now.Before(item.expiresAt) {
			delete(c.items, itemKey)
		}
	}
	if _, exists := c.items[key]; !exists && len(c.items) >= qqMediaCacheLimit {
		oldestKey := ""
		var oldestExpiry time.Time
		for itemKey, item := range c.items {
			if oldestKey == "" || item.expiresAt.Before(oldestExpiry) {
				oldestKey = itemKey
				oldestExpiry = item.expiresAt
			}
		}
		delete(c.items, oldestKey)
	}
	entry.fileInfo = cloneRawMessage(entry.fileInfo)
	c.items[key] = entry
}

func (c *qqMediaCache) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
