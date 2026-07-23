package napcat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	napcatMediaCacheTTL   = 30 * time.Minute
	napcatMediaCacheLimit = 512
)

type napcatMediaCache struct {
	mu    sync.Mutex
	items map[string]napcatMediaCacheEntry
}

type napcatMediaCacheEntry struct {
	messageID string
	expiresAt time.Time
}

func (b *Bridge) sendCachedImage(
	ctx context.Context,
	conn *websocket.Conn,
	writeMu *sync.Mutex,
	event messageEvent,
	imageURL string,
) (store.MessageAcceptance, error) {
	key, err := napcatMediaCacheKey(event, imageURL)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	if cached, ok := b.mediaCache.get(key, b.mediaNow()); ok {
		startedAt := time.Now()
		receipt, err := b.forwardCachedImage(ctx, conn, writeMu, event, cached.messageID)
		if err == nil {
			b.logf("napcat media cache hit: message_type=%q forward_ms=%d",
				event.MessageType, time.Since(startedAt).Milliseconds())
			return receipt, nil
		}
		if isUncertainSendError(err) {
			return store.MessageAcceptance{}, err
		}
		b.mediaCache.remove(key)
		b.logf("napcat cached message rejected; sending image again: message_type=%q error=%v",
			event.MessageType, err)
	} else {
		b.logf("napcat media cache miss: message_type=%q", event.MessageType)
	}

	startedAt := time.Now()
	receipt, err := b.sendImageNormally(ctx, conn, writeMu, event, imageURL)
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	receipt.DeliveryMethod = store.DeliveryMethodMediaUpload
	b.logf("napcat media sent: message_type=%q delivery_method=%q send_ms=%d",
		event.MessageType, receipt.DeliveryMethod, time.Since(startedAt).Milliseconds())
	b.mediaCache.put(key, napcatMediaCacheEntry{
		messageID: receipt.PlatformMessageID,
		expiresAt: b.mediaNow().Add(napcatMediaCacheTTL),
	}, b.mediaNow())
	return receipt, nil
}

func (b *Bridge) sendImageNormally(
	ctx context.Context,
	conn *websocket.Conn,
	writeMu *sync.Mutex,
	event messageEvent,
	imageURL string,
) (store.MessageAcceptance, error) {
	message := napcatImageMessage(imageURL)
	if conn != nil {
		return b.sendReversePayload(ctx, conn, writeMu, event, message)
	}
	return b.sendPayload(ctx, event, message)
}

func (b *Bridge) forwardCachedImage(
	ctx context.Context,
	conn *websocket.Conn,
	writeMu *sync.Mutex,
	event messageEvent,
	sourceMessageID string,
) (store.MessageAcceptance, error) {
	action := "forward_friend_single_msg"
	params := map[string]any{
		"user_id":    event.UserID,
		"message_id": sourceMessageID,
	}
	if isGroupMessageType(event.MessageType) {
		action = "forward_group_single_msg"
		params = map[string]any{
			"group_id":   event.GroupID,
			"message_id": sourceMessageID,
		}
	}

	var response napcatActionResponse
	var err error
	if conn != nil {
		response, err = b.requestReverseAction(ctx, conn, writeMu, action, params)
	} else {
		response, err = b.postActionResponse(ctx, "/"+action, params)
	}
	if err != nil {
		return store.MessageAcceptance{}, err
	}
	if err := napcatActionError(response); err != nil {
		return store.MessageAcceptance{}, err
	}
	return store.MessageAcceptance{
		DeliveryMethod:  store.DeliveryMethodForward,
		SourceMessageID: sourceMessageID,
		AcceptedAt:      b.mediaNow(),
	}, nil
}

func napcatMediaCacheKey(event messageEvent, imageURL string) (string, error) {
	scene := "private"
	if isGroupMessageType(event.MessageType) {
		scene = "group"
	}
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return "", errors.New("napcat media URL is empty")
	}
	return scene + "\x00" + imageURL, nil
}

func (b *Bridge) mediaNow() time.Time {
	if b.now != nil {
		return b.now().UTC()
	}
	return time.Now().UTC()
}

func (c *napcatMediaCache) get(key string, now time.Time) (napcatMediaCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return napcatMediaCacheEntry{}, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.items, key)
		return napcatMediaCacheEntry{}, false
	}
	return entry, true
}

func (c *napcatMediaCache) put(key string, entry napcatMediaCacheEntry, now time.Time) {
	if strings.TrimSpace(entry.messageID) == "" || !now.Before(entry.expiresAt) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]napcatMediaCacheEntry{}
	}
	for itemKey, item := range c.items {
		if !now.Before(item.expiresAt) {
			delete(c.items, itemKey)
		}
	}
	if _, exists := c.items[key]; !exists && len(c.items) >= napcatMediaCacheLimit {
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
	c.items[key] = entry
}

func (c *napcatMediaCache) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
