package responses

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	mediaIDTTL   = 24 * time.Hour
	mediaIDLimit = 1024
)

type MediaStore struct {
	baseURL string
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	items map[string]mediaItem
	ids   map[string]mediaIDEntry
}

type mediaItem struct {
	data      []byte
	expiresAt time.Time
}

type mediaIDEntry struct {
	id        string
	expiresAt time.Time
	lastUsed  time.Time
}

func NewMediaStore(baseURL string, ttl time.Duration) *MediaStore {
	return NewMediaStoreWithClock(baseURL, ttl, time.Now)
}

func NewMediaStoreWithClock(baseURL string, ttl time.Duration, now func() time.Time) *MediaStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &MediaStore{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		ttl:     ttl,
		now:     now,
		items:   map[string]mediaItem{},
		ids:     map[string]mediaIDEntry{},
	}
}

func (s *MediaStore) PutPNG(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	return s.putPNG(data, "png:"+hex.EncodeToString(sum[:]))
}

func (s *MediaStore) PutImagePNG(img *Image, data []byte) (string, error) {
	if s == nil {
		return "", errors.New("media store is nil")
	}
	contentKey, err := img.cacheKey(s.now())
	if err != nil {
		return "", err
	}
	return s.putPNG(data, "image:"+contentKey)
}

func (s *MediaStore) putPNG(data []byte, contentKey string) (string, error) {
	if s == nil {
		return "", errors.New("media store is nil")
	}
	if s.baseURL == "" {
		return "", errors.New("media base url is empty")
	}
	if len(data) == 0 {
		return "", errors.New("png data is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupExpiredLocked(now)
	entry, ok := s.ids[contentKey]
	if !ok {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			return "", err
		}
		entry.id = hex.EncodeToString(idBytes) + ".png"
	}
	entry.expiresAt = now.Add(mediaIDTTL)
	entry.lastUsed = now
	s.ids[contentKey] = entry
	s.items[entry.id] = mediaItem{
		data:      append([]byte(nil), data...),
		expiresAt: now.Add(s.ttl),
	}
	s.enforceIDLimitLocked()
	return s.baseURL + "/" + entry.id, nil
}

func (s *MediaStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/")
	id = strings.TrimPrefix(id, "media/")
	s.mu.Lock()
	item, ok := s.items[id]
	if ok && !s.now().Before(item.expiresAt) {
		delete(s.items, id)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(item.data)
}

func (s *MediaStore) cleanupExpiredLocked(now time.Time) {
	for id, item := range s.items {
		if !now.Before(item.expiresAt) {
			delete(s.items, id)
		}
	}
	for contentKey, entry := range s.ids {
		if !now.Before(entry.expiresAt) {
			delete(s.ids, contentKey)
			delete(s.items, entry.id)
		}
	}
}

func (s *MediaStore) enforceIDLimitLocked() {
	for len(s.ids) > mediaIDLimit {
		var oldestKey string
		var oldest mediaIDEntry
		for contentKey, entry := range s.ids {
			if oldestKey == "" || entry.lastUsed.Before(oldest.lastUsed) ||
				(entry.lastUsed.Equal(oldest.lastUsed) && contentKey < oldestKey) {
				oldestKey = contentKey
				oldest = entry
			}
		}
		delete(s.ids, oldestKey)
	}
}
