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

type MediaStore struct {
	baseURL string
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	items map[string]mediaItem
	ids   map[string]string
}

type mediaItem struct {
	data      []byte
	expiresAt time.Time
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
		ids:     map[string]string{},
	}
}

func (s *MediaStore) PutPNG(data []byte) (string, error) {
	if s == nil {
		return "", errors.New("media store is nil")
	}
	if s.baseURL == "" {
		return "", errors.New("media base url is empty")
	}
	if len(data) == 0 {
		return "", errors.New("png data is empty")
	}
	sum := sha256.Sum256(data)
	contentKey := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.ids[contentKey]
	if id == "" {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			return "", err
		}
		id = hex.EncodeToString(idBytes) + ".png"
		s.ids[contentKey] = id
	}
	s.items[id] = mediaItem{data: append([]byte(nil), data...), expiresAt: s.now().Add(s.ttl)}
	return s.baseURL + "/" + id, nil
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
