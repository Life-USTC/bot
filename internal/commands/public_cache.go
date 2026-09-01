package commands

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
	"golang.org/x/sync/singleflight"
)

const defaultPublicCommandCacheTTL = 5 * time.Minute

type PublicCommandCache struct {
	store   *store.Store
	version string
	ttl     time.Duration
	logger  *log.Logger
	now     func() time.Time
	group   singleflight.Group
}

func NewPublicCommandCache(stateStore *store.Store, version string, ttl time.Duration, logger *log.Logger) *PublicCommandCache {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	if ttl <= 0 {
		ttl = defaultPublicCommandCacheTTL
	}
	return &PublicCommandCache{
		store:   stateStore,
		version: version,
		ttl:     ttl,
		logger:  logger,
		now:     time.Now,
	}
}

func (c *PublicCommandCache) Purge(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.PurgePublicCommandCache(ctx, c.version, c.currentTime())
}

func (c *PublicCommandCache) GetOrLoad(ctx context.Context, command string, args []string, load func() string) string {
	if c == nil || c.store == nil || load == nil {
		if load == nil {
			return ""
		}
		return load()
	}

	command = strings.TrimSpace(command)
	argsKey := strings.Join(args, " ")
	if response, ok := c.lookup(ctx, command, argsKey); ok {
		return response
	}

	key := c.version + "\x00" + command + "\x00" + argsKey
	value, _, _ := c.group.Do(key, func() (any, error) {
		if response, ok := c.lookup(ctx, command, argsKey); ok {
			return response, nil
		}
		response := load()
		if ctx.Err() == nil && cacheablePublicCommandResponse(response) {
			now := c.currentTime()
			err := c.store.SavePublicCommandCache(ctx, store.PublicCommandCacheEntry{
				Version:   c.version,
				Command:   command,
				Args:      argsKey,
				Response:  response,
				ExpiresAt: now.Add(c.ttl),
			})
			if err != nil {
				c.logf("save public command cache: %v", err)
			}
		}
		return response, nil
	})
	response, _ := value.(string)
	return response
}

// GetOrLoadOutcome is the typed cache boundary used by capability execution.
// A cached entry only stores a successful domain response; failures are never
// guessed from their text and are never persisted by this path.
func (c *PublicCommandCache) GetOrLoadOutcome(ctx context.Context, command string, args []string, load func() CapabilityOutcome) CapabilityOutcome {
	if c == nil || c.store == nil || load == nil {
		if load == nil {
			return FailedOutcome(Response{})
		}
		return normalizeOutcome(load())
	}

	command = strings.TrimSpace(command)
	argsKey := strings.Join(args, " ")
	if response, ok := c.lookup(ctx, command, argsKey); ok {
		return SuccessOutcome(Response{Text: response})
	}

	key := c.version + "\x00" + command + "\x00" + argsKey
	value, _, _ := c.group.Do(key, func() (any, error) {
		if response, ok := c.lookup(ctx, command, argsKey); ok {
			return SuccessOutcome(Response{Text: response}), nil
		}
		outcome := normalizeOutcome(load())
		if ctx.Err() == nil && outcome.Status == CapabilityOutcomeSuccess && !outcome.ConfirmationRequired {
			now := c.currentTime()
			err := c.store.SavePublicCommandCache(ctx, store.PublicCommandCacheEntry{
				Version:   c.version,
				Command:   command,
				Args:      argsKey,
				Response:  outcome.Response.Text,
				ExpiresAt: now.Add(c.ttl),
			})
			if err != nil {
				c.logf("save public command cache: %v", err)
			}
		}
		return outcome, nil
	})
	if outcome, ok := value.(CapabilityOutcome); ok {
		return normalizeOutcome(outcome)
	}
	return FailedOutcome(Response{})
}

func (c *PublicCommandCache) lookup(ctx context.Context, command, args string) (string, bool) {
	entry, ok, err := c.store.PublicCommandCache(ctx, c.version, command, args, c.currentTime())
	if err != nil {
		c.logf("read public command cache: %v", err)
		return "", false
	}
	return entry.Response, ok
}

func (c *PublicCommandCache) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *PublicCommandCache) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

func cacheablePublicCommandResponse(response string) bool {
	response = strings.TrimSpace(response)
	if response == "" {
		return false
	}
	for _, fragment := range []string{
		"查不到：",
		"失败：",
		"unavailable",
		"网络超时",
	} {
		if strings.Contains(response, fragment) {
			return false
		}
	}
	return true
}
