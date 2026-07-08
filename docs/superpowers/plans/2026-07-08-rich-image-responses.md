# Rich Image Responses Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add feature-flagged image replies for dense bot responses while preserving the current text replies as fallback.

**Architecture:** Commands return a richer response envelope while keeping the existing `Handle(ctx, input) (string, bool)` compatibility API. A small `responses` package renders text-card PNGs and serves them through a short-lived media store. NapCat and QQ official bot adapters send image media when a response includes it and fall back to the same text reply on any media error.

**Tech Stack:** Go 1.25.10, stdlib `image/png` and `net/http`, `golang.org/x/image/font/opentype` for CJK-capable text rendering, existing NapCat OneBot 11 JSON payloads, existing QQ official bot OpenAPI HTTP client.

## Global Constraints

- Keep `commands.Handler.Handle(ctx, input) (string, bool)` as the compatibility wrapper used by existing tests and callers.
- Add `commands.Handler.HandleResponse(ctx, input) (Response, bool)` and implement `Handle` by returning `response.Text`.
- The first image-enabled reply families are schedule, todo list/filter, overview, dashboard, and upcoming deadlines.
- Mutation confirmations, login messages, help text, search results, and agent free-form answers remain text-only.
- Use `BOT_ENABLE_IMAGE_RESPONSES=false` as the default-off rollout flag.
- Use `BOT_PUBLIC_BASE_URL=https://bot.tiankaima.cn` in production so adapters can pass public media URLs to NapCat and QQ official bot.
- Generated images use short-lived in-memory storage and are not persisted indefinitely.
- Media failures must fall back to text without a user-visible image-failed message.
- Store interaction history as the text reply.
- No browser screenshot renderer.
- No Markdown retry path.

---

## File Structure

- Create `internal/responses/image.go`: response image metadata, text-card construction helpers, media URL attachment.
- Create `internal/responses/render.go`: deterministic PNG renderer using a runtime CJK font path.
- Create `internal/responses/media_store.go`: TTL media store and `http.Handler` for `/media/<id>.png`.
- Create `internal/responses/render_test.go`: PNG decode and deterministic dimension coverage.
- Create `internal/responses/media_store_test.go`: TTL, content type, and 404 coverage.
- Create `internal/commands/response.go`: `commands.Response` and command-to-image selection helpers.
- Modify `internal/commands/commands.go`: add `Handler.EnableImageResponses`; add `HandleResponse`; keep `Handle` unchanged externally.
- Modify `internal/commands/commands_test.go`: command response metadata tests using existing Life API fixtures.
- Modify `internal/config/config.go` and `internal/config/config_test.go`: parse `BOT_ENABLE_IMAGE_RESPONSES`, `BOT_PUBLIC_BASE_URL`, `BOT_MEDIA_ADDR`, `BOT_IMAGE_FONT_PATH`, and `BOT_MEDIA_TTL_SECONDS`.
- Modify `cmd/life-ustc-bot/main.go`: start the media HTTP server when image responses are enabled and wire renderer/store into platform adapters.
- Modify `internal/napcat/bridge.go` and `internal/napcat/bridge_test.go`: add image segment send path with text fallback.
- Modify `internal/qqbot/bot.go` and `internal/qqbot/bot_test.go`: add rich media upload/send path with text fallback.
- Modify `Dockerfile`: install a CJK font package in the runtime image.
- Modify `README.md`: document the new feature flag and media serving environment variables.

## Task 1: Rich Command Response Envelope

**Files:**
- Create: `internal/responses/image.go`
- Create: `internal/commands/response.go`
- Modify: `internal/commands/commands.go`
- Test: `internal/commands/commands_test.go`

**Interfaces:**
- Produces: `commands.Response`, `commands.Handler.HandleResponse(ctx context.Context, input commands.Input) (commands.Response, bool)`, `responses.Image`.
- Consumes later: NapCat and QQ official bot adapters read `Response.Text` and `Response.Image`.

- [ ] **Step 1: Write failing command response tests**

Add these tests to `internal/commands/commands_test.go` near the existing schedule/dashboard tests:

```go
func TestHandleResponseKeepsHandleTextCompatibility(t *testing.T) {
	ctx := context.Background()
	handler := Handler{Prefix: "/life", EnableImageResponses: true}

	response, ok := handler.HandleResponse(ctx, Input{Text: "/help", Identity: testIdentity()})
	if !ok {
		t.Fatal("command was not handled")
	}
	text, ok := handler.Handle(ctx, Input{Text: "/help", Identity: testIdentity()})
	if !ok {
		t.Fatal("Handle did not handle the command")
	}
	if response.Text != text {
		t.Fatalf("HandleResponse text = %q, Handle text = %q", response.Text, text)
	}
	if response.Image != nil {
		t.Fatalf("help response image = %#v, want nil", response.Image)
	}
}

func TestHandleResponseAddsImageForEnabledSchedule(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/api/me/subscriptions/schedules" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"schedules":[{"startTime":"09:50","endTime":"11:25","section":{"course":{"namePrimary":"数据库系统"}},"room":{"namePrimary":"西区 3A204"}}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	handler.EnableImageResponses = true
	response, ok := handler.HandleResponse(ctx, Input{Text: "今天课表", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "数据库系统") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image == nil {
		t.Fatal("image = nil, want schedule image")
	}
	if response.Image.Kind != "schedule" || response.Image.Title != "今天课表" {
		t.Fatalf("image = %#v", response.Image)
	}
	if !strings.Contains(response.Image.AltText, "数据库系统") {
		t.Fatalf("alt text = %q", response.Image.AltText)
	}
}

func TestHandleResponseDoesNotAddImageWhenDisabled(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"todos":[{"id":"todo-1","title":"写报告","priority":"high"}]}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	response, ok := handler.HandleResponse(ctx, Input{Text: "td", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "写报告") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image != nil {
		t.Fatalf("image = %#v, want nil", response.Image)
	}
}

func TestHandleResponseLeavesTodoMutationTextOnly(t *testing.T) {
	ctx := context.Background()
	ident := testIdentity()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/todos" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"todo-1","title":"写报告"}`))
	}))
	defer server.Close()

	handler := testAuthedHandler(t, server, ident)
	handler.EnableImageResponses = true
	response, ok := handler.HandleResponse(ctx, Input{Text: "td 写报告", Identity: ident})
	if !ok {
		t.Fatal("command was not handled")
	}
	if !strings.Contains(response.Text, "已加待办：写报告") {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Image != nil {
		t.Fatalf("todo mutation image = %#v, want nil", response.Image)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/commands -run 'TestHandleResponse' -count=1
```

Expected: FAIL because `Handler.HandleResponse`, `Handler.EnableImageResponses`, and `Response.Image` do not exist.

- [ ] **Step 3: Add response metadata types**

Create `internal/responses/image.go`:

```go
package responses

import "strings"

type Image struct {
	Kind    string
	Title   string
	Lines   []string
	AltText string
	URL     string
}

func NewTextImage(kind, title, text string) *Image {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return &Image{
		Kind:    strings.TrimSpace(kind),
		Title:   strings.TrimSpace(title),
		Lines:   lines,
		AltText: text,
	}
}

func (img *Image) WithURL(url string) *Image {
	if img == nil {
		return nil
	}
	next := *img
	next.URL = strings.TrimSpace(url)
	return &next
}
```

Create `internal/commands/response.go`:

```go
package commands

import (
	"strings"

	"github.com/Life-USTC/Bot/internal/responses"
)

type Response struct {
	Text  string
	Image *responses.Image
	Kind  string
}

func textResponse(text string) Response {
	return Response{Text: text}
}

func (h Handler) imageResponseFor(cmd parsedCommand, text string) *responses.Image {
	if !h.EnableImageResponses || strings.TrimSpace(text) == "" {
		return nil
	}
	if !successfulImageText(text) {
		return nil
	}
	switch cmd.Name {
	case "schedule":
		if firstArgIs(cmd.Args, "help") {
			return nil
		}
		return responses.NewTextImage("schedule", imageTitle(text, "课表"), text)
	case "todo":
		if !todoImageArgs(cmd.Args) {
			return nil
		}
		return responses.NewTextImage("todo", imageTitle(text, "待办"), text)
	case "overview":
		return responses.NewTextImage("overview", imageTitle(text, "今日安排"), text)
	case "dashboard":
		return responses.NewTextImage("dashboard", imageTitle(text, "我的概览"), text)
	case "upcoming_deadlines":
		return responses.NewTextImage("deadlines", imageTitle(text, "近期截止"), text)
	default:
		return nil
	}
}

func imageTitle(text, fallback string) string {
	first := strings.TrimSpace(strings.Split(strings.TrimSpace(text), "\n")[0])
	first = strings.TrimSuffix(first, "：")
	first = strings.TrimSuffix(first, ":")
	if first == "" {
		return fallback
	}
	return first
}

func successfulImageText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	rejectPrefixes := []string{
		"需要先登录",
		"登录未配置",
		"Life @ USTC API unavailable",
		"课表查不到：",
		"待办查不到：",
		"今日安排查不到：",
		"概览查不到：",
		"近期截止查不到：",
	}
	for _, prefix := range rejectPrefixes {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}
	return true
}

func todoImageArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "list", "all", "pending", "completed":
		return true
	default:
		_, ok := normalizeTodoPriority(args[0])
		return ok
	}
}
```

- [ ] **Step 4: Add `HandleResponse` and keep `Handle` compatible**

Modify `internal/commands/commands.go`:

```go
type Handler struct {
	Life                   *life.Client
	Auth                   *auth.Manager
	Store                  *store.Store
	Prefix                 string
	Logger                 *log.Logger
	FeedbackUsers          []string
	FeedbackGroups         []string
	FeedbackSend           func(context.Context, store.Identity, string) error
	AllowGroupPersonalInfo bool
	EnableImageResponses   bool
}
```

Replace the current `Handle` body with a wrapper and move the existing body into `HandleResponse`:

```go
func (h Handler) Handle(ctx context.Context, input Input) (string, bool) {
	response, ok := h.HandleResponse(ctx, input)
	return response.Text, ok
}

func (h Handler) HandleResponse(ctx context.Context, input Input) (Response, bool) {
	if isConfirmationOK(input.Text) {
		if reply, ok := h.confirmPending(ctx, input); ok {
			return textResponse(reply), true
		}
	}
	cmd, ok := h.parse(input.Text)
	if !ok && store.IsGroupConversation(input.Identity) {
		cmd, ok = parseGroupBus(input.Text)
	}
	if !ok {
		return Response{}, false
	}
	if store.IsGroupConversation(input.Identity) && !h.groupCommandAllowed(cmd) {
		return Response{}, false
	}
	if h.hasAdditionalCommandLine(input.Text) {
		reply := "检测到多条命令。为避免误操作，一次只处理一条；请分开发送。"
		if !input.SuppressLog {
			h.recordState(ctx, input.Identity, cmd)
			h.recordInteraction(ctx, input.Identity, cmd, reply)
		}
		return textResponse(reply), true
	}
	if !input.SuppressLog {
		h.recordState(ctx, input.Identity, cmd)
	}
	var reply string
	if cmd.Name == "help" {
		reply = h.help()
	} else {
		spec, ok := commandSpec(cmd.Name)
		if !ok || spec.Run == nil {
			reply = h.help()
		} else if firstArgIs(cmd.Args, "help") && !spec.HasHelp {
			reply = h.help()
		} else if spec.NeedsLife && h.Life == nil && !firstArgIs(cmd.Args, "help") {
			reply = "Life @ USTC API unavailable: not configured."
		} else if spec.NeedsAuth && (h.Auth == nil || h.Auth.Store == nil) && !firstArgIs(cmd.Args, "help") {
			reply = "登录未配置。"
		} else if spec.NeedsStore && h.Store == nil && !firstArgIs(cmd.Args, "help") {
			reply = "存储未配置。"
		} else {
			reply = spec.Run(h, ctx, input.Identity, cmd.Args)
		}
	}
	if !input.SuppressLog {
		h.recordInteraction(ctx, input.Identity, cmd, reply)
	}
	return Response{Text: reply, Image: h.imageResponseFor(cmd, reply), Kind: cmd.Name}, true
}
```

- [ ] **Step 5: Run command tests**

Run:

```bash
go test ./internal/commands -run 'TestHandleResponse|TestHandleTodayCurriculum|TestHandleTodo|TestHandleDashboard|TestHandleUpcomingDeadlines' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/responses/image.go internal/commands/response.go internal/commands/commands.go internal/commands/commands_test.go
git commit -m "feat(bot): add rich command response envelope"
```

## Task 2: PNG Renderer and TTL Media Store

**Files:**
- Create: `internal/responses/render.go`
- Create: `internal/responses/media_store.go`
- Create: `internal/responses/render_test.go`
- Create: `internal/responses/media_store_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `Dockerfile`

**Interfaces:**
- Consumes: `responses.Image` from Task 1.
- Produces: `responses.Renderer.RenderPNG(img *responses.Image) ([]byte, int, int, error)`, `responses.MediaStore.PutPNG(png []byte) (string, error)`, `responses.MediaStore.ServeHTTP`.

- [ ] **Step 1: Add dependency**

Run:

```bash
go get golang.org/x/image@v0.43.0
```

Expected: `go.mod` gains `golang.org/x/image` and `go.sum` is updated.

- [ ] **Step 2: Write failing renderer tests**

Create `internal/responses/render_test.go`:

```go
package responses

import (
	"bytes"
	"image/png"
	"testing"
)

func TestRendererCreatesValidPNG(t *testing.T) {
	renderer := Renderer{FontPath: testFontPath(t)}
	img := NewTextImage("schedule", "今天课表", "今天课表：\n西区 3A204\t09:50-11:25\t数据库系统")

	data, width, height, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || width <= 0 || height <= 0 {
		t.Fatalf("len=%d width=%d height=%d", len(data), width, height)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Fatalf("bounds = %v width=%d height=%d", decoded.Bounds(), width, height)
	}
}

func TestRendererDimensionsAreStable(t *testing.T) {
	renderer := Renderer{FontPath: testFontPath(t)}
	img := NewTextImage("todo", "待办", "待办：\n1. 截止 07-08 写报告\n2. 买咖啡")

	_, width1, height1, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	_, width2, height2, err := renderer.RenderPNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if width1 != width2 || height1 != height2 {
		t.Fatalf("first=%dx%d second=%dx%d", width1, height1, width2, height2)
	}
}

func testFontPath(t *testing.T) string {
	t.Helper()
	for _, path := range defaultFontPaths {
		if fileExists(path) {
			return path
		}
	}
	t.Skip("no CJK font found")
	return ""
}
```

- [ ] **Step 3: Write failing media store tests**

Create `internal/responses/media_store_test.go`:

```go
package responses

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMediaStoreServesPNGBeforeTTL(t *testing.T) {
	store := NewMediaStore("https://bot.example/media", time.Minute)
	url, err := store.PutPNG([]byte{0x89, 'P', 'N', 'G'})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(url, "https://bot.example")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	store.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string([]byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("body = %#v", body)
	}
}

func TestMediaStoreExpiresPNG(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	store := NewMediaStoreWithClock("https://bot.example/media", time.Second, func() time.Time { return now })
	url, err := store.PutPNG([]byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	path := strings.TrimPrefix(url, "https://bot.example")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()

	store.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
```

Run:

```bash
go test ./internal/responses -count=1
```

Expected: FAIL because renderer and media store types do not exist.

- [ ] **Step 4: Implement renderer**

Create `internal/responses/render.go` with these exported types and helpers:

```go
package responses

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type Renderer struct {
	FontPath string
}

var defaultFontPaths = []string{
	"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-fonts/NotoSansCJK-Regular.ttc",
	"/usr/share/fonts/google-noto-sans-cjk-vf-fonts/NotoSansCJK-VF.ttc",
	"/usr/share/fonts/adobe-source-han-sans-cn-fonts/SourceHanSansCN-Regular.otf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
}

func (r Renderer) RenderPNG(img *Image) ([]byte, int, int, error) {
	if img == nil || strings.TrimSpace(img.AltText) == "" {
		return nil, 0, 0, errors.New("response image is empty")
	}
	face, err := r.fontFace(30)
	if err != nil {
		return nil, 0, 0, err
	}
	titleFace, err := r.fontFace(38)
	if err != nil {
		return nil, 0, 0, err
	}
	lines := wrappedLines(img.Lines, 36)
	width := 900
	height := 72 + 56 + len(lines)*42 + 44
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{248, 250, 252, 255}}, image.Point{}, draw.Src)
	card := image.Rect(28, 28, width-28, height-28)
	draw.Draw(canvas, card, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	drawText(canvas, titleFace, 56, 82, img.Title, color.RGBA{15, 23, 42, 255})
	y := 136
	for _, line := range lines {
		drawText(canvas, face, 56, y, line, color.RGBA{30, 41, 59, 255})
		y += 42
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), width, height, nil
}

func (r Renderer) fontFace(size float64) (font.Face, error) {
	path := strings.TrimSpace(r.FontPath)
	if path == "" {
		for _, candidate := range defaultFontPaths {
			if fileExists(candidate) {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return nil, errors.New("no font path configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		collection, collectionErr := opentype.ParseCollection(data)
		if collectionErr != nil || collection.NumFonts() == 0 {
			return nil, err
		}
		parsed, err = collection.Font(0)
		if err != nil {
			return nil, err
		}
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func drawText(dst *image.RGBA, face font.Face, x, y int, text string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func wrappedLines(lines []string, maxRunes int) []string {
	out := []string{}
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		runes := []rune(line)
		for len(runes) > maxRunes {
			out = append(out, string(runes[:maxRunes]))
			runes = runes[maxRunes:]
		}
		out = append(out, string(runes))
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
```

- [ ] **Step 5: Implement media store**

Create `internal/responses/media_store.go`:

```go
package responses

import (
	"crypto/rand"
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
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes) + ".png"
	s.mu.Lock()
	s.items[id] = mediaItem{data: append([]byte(nil), data...), expiresAt: s.now().Add(s.ttl)}
	s.mu.Unlock()
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
```

- [ ] **Step 6: Install CJK font in runtime Docker image**

Modify `Dockerfile` runtime stage before `USER 10001:10001`:

```dockerfile
RUN apt-get update \
	&& apt-get install -y --no-install-recommends fonts-noto-cjk \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /data \
	&& chown 10001:10001 /data
```

This replaces the existing `RUN mkdir -p /data ...` block.

- [ ] **Step 7: Run response tests**

Run:

```bash
go test ./internal/responses -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum Dockerfile internal/responses/render.go internal/responses/render_test.go internal/responses/media_store.go internal/responses/media_store_test.go
git commit -m "feat(bot): render rich responses as png"
```

## Task 3: Configuration and Media Server Wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/life-ustc-bot/main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `responses.MediaStore` and `responses.Renderer` from Task 2.
- Produces: `Config.EnableImageResponses`, `Config.PublicBaseURL`, `Config.MediaAddr`, `Config.ImageFontPath`, `Config.MediaTTL`.

- [ ] **Step 1: Write failing config test**

Add to `internal/config/config_test.go`:

```go
func TestFromEnvParsesImageResponseConfig(t *testing.T) {
	t.Setenv("BOT_ENABLE_IMAGE_RESPONSES", "true")
	t.Setenv("BOT_PUBLIC_BASE_URL", " https://bot.example/// ")
	t.Setenv("BOT_MEDIA_ADDR", " 127.0.0.1:2281 ")
	t.Setenv("BOT_IMAGE_FONT_PATH", " /fonts/NotoSansCJK-Regular.ttc ")
	t.Setenv("BOT_MEDIA_TTL_SECONDS", "180")

	cfg := FromEnv()
	if !cfg.EnableImageResponses {
		t.Fatal("EnableImageResponses = false, want true")
	}
	if cfg.PublicBaseURL != "https://bot.example" {
		t.Fatalf("PublicBaseURL = %q", cfg.PublicBaseURL)
	}
	if cfg.MediaAddr != "127.0.0.1:2281" {
		t.Fatalf("MediaAddr = %q", cfg.MediaAddr)
	}
	if cfg.ImageFontPath != "/fonts/NotoSansCJK-Regular.ttc" {
		t.Fatalf("ImageFontPath = %q", cfg.ImageFontPath)
	}
	if cfg.MediaTTL != 180*time.Second {
		t.Fatalf("MediaTTL = %s", cfg.MediaTTL)
	}
}
```

Run:

```bash
go test ./internal/config -run TestFromEnvParsesImageResponseConfig -count=1
```

Expected: FAIL because the config fields do not exist.

- [ ] **Step 2: Add config fields and parsing**

Modify `internal/config/config.go`:

```go
type Config struct {
	LifeServer             string
	OneBotHTTPHost         string
	OneBotHTTPPort         uint16
	OneBotAccessToken      string
	OneBotSelfID           string
	NapCatAPIURL           string
	NapCatAccessToken      string
	NapCatWSURL            string
	NapCatReverseAddr      string
	NapCatReversePath      string
	QQBotAppID             string
	QQBotAppSecret         string
	QQBotToken             string
	QQBotID                string
	QQBotAPIBaseURL        string
	QQBotTokenURL          string
	QQBotGatewayURL        string
	QQBotWebhookAddr       string
	QQBotWebhookPath       string
	QQBotIntents           uint64
	DBPath                 string
	CommandPrefix          string
	HTTPClientTimeout      time.Duration
	EnableOneBotServer     bool
	EnableNapCatBridge     bool
	EnableQQBot            bool
	EnableQQBotGateway     bool
	EnableQQBotWebhook     bool
	EnableAgent            bool
	EnableImageResponses   bool
	AllowGroupPersonalInfo bool
	PublicBaseURL          string
	MediaAddr              string
	ImageFontPath          string
	MediaTTL               time.Duration
	LLMAPIKey              string
	LLMBaseURL             string
	LLMModel               string
	LLMTimeout             time.Duration
	FeedbackAdminUsers     []string
	FeedbackAdminGroups    []string
}
```

Add to `FromEnv()`:

```go
EnableImageResponses:   envBool("BOT_ENABLE_IMAGE_RESPONSES", false),
PublicBaseURL:          envTrimRight("BOT_PUBLIC_BASE_URL", "", "/"),
MediaAddr:              envString("BOT_MEDIA_ADDR", "127.0.0.1:2281"),
ImageFontPath:          envOptionalString("BOT_IMAGE_FONT_PATH"),
MediaTTL:               time.Duration(envPositiveInt("BOT_MEDIA_TTL_SECONDS", 300)) * time.Second,
```

- [ ] **Step 3: Wire renderer and media store into main**

Modify `cmd/life-ustc-bot/main.go` imports:

```go
	"time"

	"github.com/Life-USTC/Bot/internal/responses"
```

Add after `authManager` setup:

```go
	var mediaStore *responses.MediaStore
	renderer := responses.Renderer{FontPath: cfg.ImageFontPath}
	if cfg.EnableImageResponses && cfg.PublicBaseURL != "" {
		mediaStore = responses.NewMediaStore(strings.TrimRight(cfg.PublicBaseURL, "/")+"/media", cfg.MediaTTL)
		mediaServer := &http.Server{Addr: cfg.MediaAddr, Handler: mediaStore}
		go func() {
			if err := mediaServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Printf("media server stopped: %v", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = mediaServer.Shutdown(shutdownCtx)
		}()
		logger.Printf("Media server listening on %s", cfg.MediaAddr)
	} else if cfg.EnableImageResponses {
		logger.Printf("Image responses disabled: BOT_PUBLIC_BASE_URL is empty")
	}
```

Set the handler field:

```go
EnableImageResponses: cfg.EnableImageResponses && mediaStore != nil,
```

When constructing `napcat.Bridge` and `qqbot.Bot`, add:

```go
Renderer:   renderer,
MediaStore: mediaStore,
```

- [ ] **Step 4: Document environment variables**

Add to the README environment block:

```text
BOT_ENABLE_IMAGE_RESPONSES=false
BOT_PUBLIC_BASE_URL=
BOT_MEDIA_ADDR=127.0.0.1:2281
BOT_IMAGE_FONT_PATH=
BOT_MEDIA_TTL_SECONDS=300
```

Add one paragraph after the QQ official bot section:

```markdown
Set `BOT_ENABLE_IMAGE_RESPONSES=true` plus `BOT_PUBLIC_BASE_URL=https://<public-host>`
to let schedule, todo list, overview, dashboard, and upcoming-deadline replies
send a short-lived PNG image before falling back to text. Route
`/media/*` from the public host to `BOT_MEDIA_ADDR`. The runtime image installs
`fonts-noto-cjk`; set `BOT_IMAGE_FONT_PATH` only when overriding the default font.
```

- [ ] **Step 5: Run config tests**

Run:

```bash
go test ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/life-ustc-bot/main.go README.md
git commit -m "feat(bot): configure image response media server"
```

## Task 4: NapCat Image Send and Fallback

**Files:**
- Modify: `internal/napcat/bridge.go`
- Modify: `internal/napcat/bridge_test.go`

**Interfaces:**
- Consumes: `commands.Response`, `responses.Renderer`, `responses.MediaStore`.
- Produces: `Bridge.SendResponse(ctx, event, response)` and `sendReverseResponse`.

- [ ] **Step 1: Write failing NapCat HTTP image test**

Add to `internal/napcat/bridge_test.go`:

```go
func TestSendResponsePostsImageSegmentWhenAvailable(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_private_msg" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bridge := Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	response := commands.Response{
		Text:  "今天课表：\n数据库系统",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表：\n数据库系统"),
	}

	err := bridge.SendResponse(context.Background(), messageEvent{MessageType: "private", UserID: 456}, response)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := gotBody["message"].([]any)
	if !ok || len(message) != 1 {
		t.Fatalf("message = %#v", gotBody["message"])
	}
	segment := message[0].(map[string]any)
	if segment["type"] != "image" {
		t.Fatalf("segment = %#v", segment)
	}
	data := segment["data"].(map[string]any)
	if !strings.HasPrefix(data["file"].(string), server.URL+"/media/") {
		t.Fatalf("file = %q", data["file"])
	}
}
```

Add the helper at the end of the file:

```go
func testResponseFontPath(t *testing.T) string {
	t.Helper()
	for _, path := range responses.DefaultFontPathsForTest() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skip("no CJK font found")
	return ""
}
```

If `DefaultFontPathsForTest` is not exported in Task 2, add this small helper to `internal/responses/render.go`:

```go
func DefaultFontPathsForTest() []string {
	return append([]string(nil), defaultFontPaths...)
}
```

- [ ] **Step 2: Write failing NapCat fallback test**

Add to `internal/napcat/bridge_test.go`:

```go
func TestSendResponseFallsBackToTextWhenImagePostFails(t *testing.T) {
	requests := 0
	var fallbackBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"status":"failed","message":"image rejected"}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&fallbackBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	store := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bridge := Bridge{
		APIURL:     server.URL,
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: store,
	}
	response := commands.Response{
		Text:  "待办：\n1. 写报告",
		Image: responses.NewTextImage("todo", "待办", "待办：\n1. 写报告"),
	}

	err := bridge.SendResponse(context.Background(), messageEvent{MessageType: "private", UserID: 456}, response)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if fallbackBody["message"] != response.Text {
		t.Fatalf("fallback body = %#v", fallbackBody)
	}
}
```

Run:

```bash
go test ./internal/napcat -run 'TestSendResponse' -count=1
```

Expected: FAIL because `Bridge.Renderer`, `Bridge.MediaStore`, and `SendResponse` do not exist.

- [ ] **Step 3: Implement NapCat response sending**

Modify `internal/napcat/bridge.go`:

```go
type Bridge struct {
	APIURL      string
	AccessToken string
	WSURL       string
	Handler     commands.Handler
	Agent       *agent.Service
	HTTPClient  *http.Client
	Logger      *log.Logger
	Renderer    responses.Renderer
	MediaStore  *responses.MediaStore

	reverseMu      sync.Mutex
	reverseConn    *websocket.Conn
	reverseWriteMu *sync.Mutex
	reverseSeq     uint64
}
```

Change `handleMessage` to return `commands.Response`:

```go
func (b *Bridge) handleMessage(ctx context.Context, event messageEvent) (commands.Response, bool) {
	response, ok := b.Handler.HandleResponse(ctx, commands.Input{
		Text:     event.RawMessage,
		Identity: event.identity(),
	})
	if !ok {
		reply, agentOK := b.handleAgent(ctx, event)
		if agentOK {
			return commands.Response{Text: reply, Kind: "agent"}, true
		}
	}
	if !ok {
		b.recordIgnored(ctx, event)
		return commands.Response{}, false
	}
	return response, true
}
```

Update call sites to use `response.Text` for logs/storage and `SendResponse` for sending:

```go
if err := b.SendResponse(ctx, event, reply); err != nil {
	b.logf("send reply failed: %v", err)
}
```

Add:

```go
func (b *Bridge) SendResponse(ctx context.Context, event messageEvent, response commands.Response) error {
	if response.Image != nil && b.MediaStore != nil {
		if url, err := b.prepareImageURL(response.Image); err == nil {
			if conn, writeMu := b.activeReverseConn(); conn != nil {
				if err := sendReversePayload(conn, writeMu, event, napcatImageMessage(url)); err == nil {
					b.recordOutbound(ctx, event, response.Text, store.InteractionStatusSent, nil)
					return nil
				} else {
					b.logf("reverse websocket image send failed: %v", err)
				}
			} else if err := b.sendPayload(ctx, event, napcatImageMessage(url)); err == nil {
				b.recordOutbound(ctx, event, response.Text, store.InteractionStatusSent, nil)
				return nil
			} else {
				b.logf("napcat image send failed: %v", err)
			}
		} else {
			b.logf("prepare napcat image failed: %v", err)
		}
	}
	return b.Send(ctx, event, response.Text)
}

func (b *Bridge) prepareImageURL(img *responses.Image) (string, error) {
	if img.URL != "" {
		return img.URL, nil
	}
	data, _, _, err := b.Renderer.RenderPNG(img)
	if err != nil {
		return "", err
	}
	return b.MediaStore.PutPNG(data)
}

func napcatImageMessage(url string) []map[string]any {
	return []map[string]any{{
		"type": "image",
		"data": map[string]any{"file": url},
	}}
}
```

Refactor payload helpers:

```go
func (b *Bridge) sendPayload(ctx context.Context, event messageEvent, message any) error {
	endpoint := "/send_private_msg"
	payload := map[string]any{"user_id": event.UserID, "message": message}
	if isGroupMessageType(event.MessageType) {
		endpoint = "/send_group_msg"
		payload = map[string]any{"group_id": event.GroupID, "message": message}
	}
	return b.post(ctx, endpoint, payload)
}

func sendReversePayload(conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, message any) error {
	action := "send_private_msg"
	params := map[string]any{"user_id": event.UserID, "message": message}
	if isGroupMessageType(event.MessageType) {
		action = "send_group_msg"
		params = map[string]any{"group_id": event.GroupID, "message": message}
	}
	frame := map[string]any{
		"action": action,
		"params": params,
		"echo":   fmt.Sprintf("life-ustc-%d", atomic.AddUint64(&echoCounter, 1)),
	}
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	return conn.WriteJSON(frame)
}

func sendReverseReply(conn *websocket.Conn, writeMu *sync.Mutex, event messageEvent, message string) error {
	return sendReversePayload(conn, writeMu, event, message)
}
```

Keep `Send`, `SendMessage`, and `SendLoginMessage` string-based for login and notifications.

- [ ] **Step 4: Run NapCat tests**

Run:

```bash
go test ./internal/napcat -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/napcat/bridge.go internal/napcat/bridge_test.go internal/responses/render.go
git commit -m "feat(bot): send napcat image responses"
```

## Task 5: QQ Official Bot Rich Media Upload and Fallback

**Files:**
- Modify: `internal/qqbot/bot.go`
- Modify: `internal/qqbot/bot_test.go`

**Interfaces:**
- Consumes: `commands.Response`, `responses.Renderer`, `responses.MediaStore`.
- Produces: QQ official rich media upload to `/v2/users/{id}/files` or `/v2/groups/{id}/files`, then message send with `msg_type: 7` and `media.file_info`.

- [ ] **Step 1: Write failing QQ rich media test**

Add to `internal/qqbot/bot_test.go`:

```go
func TestSendResponseUploadsAndSendsC2CImage(t *testing.T) {
	var uploaded map[string]any
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/users/user-openid/files":
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-token"})
		case "/v2/users/user-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	message := &incomingMessage{
		ID:   "message-id",
		Type: "C2C_MESSAGE_CREATE",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "user-openid",
			ConversationType: "private",
			ConversationID:   "user-openid",
		},
	}
	response := commands.Response{
		Text:  "今天课表：\n数据库系统",
		Image: responses.NewTextImage("schedule", "今天课表", "今天课表：\n数据库系统"),
	}

	err := bot.SendResponse(context.Background(), message, response)
	if err != nil {
		t.Fatal(err)
	}
	if uploaded["file_type"].(float64) != 1 || uploaded["srv_send_msg"].(bool) {
		t.Fatalf("upload body = %#v", uploaded)
	}
	if !strings.HasPrefix(uploaded["url"].(string), server.URL+"/media/") {
		t.Fatalf("upload url = %q", uploaded["url"])
	}
	if sent["msg_type"].(float64) != 7 {
		t.Fatalf("sent body = %#v", sent)
	}
	media := sent["media"].(map[string]any)
	if media["file_info"] != "file-token" {
		t.Fatalf("media = %#v", media)
	}
	if sent["msg_id"] != "message-id" || sent["msg_seq"].(float64) != 1 {
		t.Fatalf("passive fields = %#v", sent)
	}
}
```

- [ ] **Step 2: Write failing QQ fallback test**

Add to `internal/qqbot/bot_test.go`:

```go
func TestSendResponseFallsBackToTextWhenQQImageUploadFails(t *testing.T) {
	var textBody sendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "expires_in": 7200})
		case "/v2/groups/group-openid/files":
			http.Error(w, "upload rejected", http.StatusBadRequest)
		case "/v2/groups/group-openid/messages":
			if err := json.NewDecoder(r.Body).Decode(&textBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	mediaStore := responses.NewMediaStore(server.URL+"/media", time.Minute)
	bot := &Bot{
		AppID:      "appid",
		AppSecret:  "secret",
		APIBaseURL: server.URL,
		TokenURL:   server.URL + "/app/getAppAccessToken",
		HTTPClient: server.Client(),
		Renderer:   responses.Renderer{FontPath: testResponseFontPath(t)},
		MediaStore: mediaStore,
	}
	message := &incomingMessage{
		ID:   "message-id",
		Type: "GROUP_AT_MESSAGE_CREATE",
		Identity: store.Identity{
			Platform:         "qqbot",
			UserID:           "member-openid",
			ConversationType: "group",
			ConversationID:   "group-openid",
		},
	}
	response := commands.Response{
		Text:  "待办：\n1. 写报告",
		Image: responses.NewTextImage("todo", "待办", "待办：\n1. 写报告"),
	}

	err := bot.SendResponse(context.Background(), message, response)
	if err != nil {
		t.Fatal(err)
	}
	if textBody.MsgType != 0 || textBody.Content != "\n\n"+response.Text {
		t.Fatalf("text fallback body = %#v", textBody)
	}
}
```

Run:

```bash
go test ./internal/qqbot -run 'TestSendResponse' -count=1
```

Expected: FAIL because QQ rich response fields and methods do not exist.

- [ ] **Step 3: Implement QQ rich media types and methods**

Modify `internal/qqbot/bot.go` imports:

```go
	"encoding/json"

	"github.com/Life-USTC/Bot/internal/responses"
```

Add fields:

```go
	Renderer   responses.Renderer
	MediaStore *responses.MediaStore
```

Extend `sendMessageRequest`:

```go
type sendMessageRequest struct {
	Content string     `json:"content,omitempty"`
	MsgType int        `json:"msg_type"`
	MsgID   string     `json:"msg_id,omitempty"`
	EventID string     `json:"event_id,omitempty"`
	MsgSeq  int        `json:"msg_seq,omitempty"`
	Media   *mediaInfo `json:"media,omitempty"`
}

type mediaInfo struct {
	FileInfo json.RawMessage `json:"file_info,omitempty"`
}

type richMediaUploadRequest struct {
	FileType   int    `json:"file_type"`
	URL        string `json:"url"`
	SrvSendMsg bool   `json:"srv_send_msg"`
}

type richMediaUploadResponse struct {
	FileInfo json.RawMessage `json:"file_info"`
}
```

Add:

```go
func (b *Bot) SendResponse(ctx context.Context, message *incomingMessage, response commands.Response) error {
	if message == nil {
		return errors.New("qq bot message is nil")
	}
	if response.Image != nil && b.MediaStore != nil {
		if err := b.sendImageResponse(ctx, message, response); err == nil {
			b.recordOutbound(ctx, message.Identity, response.Text, store.InteractionStatusSent, nil)
			return nil
		} else {
			b.logf("QQ bot image response failed: %v", err)
		}
	}
	return b.Send(ctx, message, response.Text)
}

func (b *Bot) sendImageResponse(ctx context.Context, message *incomingMessage, response commands.Response) error {
	url, err := b.prepareImageURL(response.Image)
	if err != nil {
		return err
	}
	fileInfo, err := b.uploadRichMedia(ctx, message.Identity, url)
	if err != nil {
		return err
	}
	return b.sendRichMediaTo(ctx, message.Identity, fileInfo, message.ID, message.EventID, message.nextReplySeq())
}

func (b *Bot) prepareImageURL(img *responses.Image) (string, error) {
	if img.URL != "" {
		return img.URL, nil
	}
	data, _, _, err := b.Renderer.RenderPNG(img)
	if err != nil {
		return "", err
	}
	return b.MediaStore.PutPNG(data)
}
```

Add upload and send helpers:

```go
func (b *Bot) uploadRichMedia(ctx context.Context, ident store.Identity, imageURL string) (json.RawMessage, error) {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return nil, err
	}
	path, err := richMediaUploadPath(ident)
	if err != nil {
		return nil, err
	}
	var out richMediaUploadResponse
	err = b.openAPI(ctx, http.MethodPost, path, token, richMediaUploadRequest{
		FileType:   1,
		URL:        imageURL,
		SrvSendMsg: false,
	}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.FileInfo) == 0 {
		return nil, errors.New("qq bot rich media upload missing file_info")
	}
	return out.FileInfo, nil
}

func (b *Bot) sendRichMediaTo(ctx context.Context, ident store.Identity, fileInfo json.RawMessage, msgID, eventID string, msgSeq int) error {
	token, err := b.accessTokenForRequest(ctx)
	if err != nil {
		return err
	}
	path, err := sendPath(ident)
	if err != nil {
		return err
	}
	body := sendMessageRequest{
		MsgType: 7,
		Media:  &mediaInfo{FileInfo: fileInfo},
	}
	if strings.TrimSpace(msgID) != "" {
		body.MsgID = strings.TrimSpace(msgID)
		body.MsgSeq = msgSeq
	}
	if strings.TrimSpace(eventID) != "" {
		body.EventID = strings.TrimSpace(eventID)
	}
	return b.openAPI(ctx, http.MethodPost, path, token, body, nil)
}

func richMediaUploadPath(ident store.Identity) (string, error) {
	switch textutil.LowerTrim(ident.ConversationType) {
	case "group":
		groupID := strings.TrimSpace(ident.ConversationID)
		if groupID == "" {
			return "", errors.New("qq bot group openid is empty")
		}
		return "/v2/groups/" + url.PathEscape(groupID) + "/files", nil
	case "private", "":
		openID := textutil.FirstNonEmpty(ident.ConversationID, ident.UserID)
		if openID == "" {
			return "", errors.New("qq bot user openid is empty")
		}
		return "/v2/users/" + url.PathEscape(openID) + "/files", nil
	default:
		return "", fmt.Errorf("qq bot rich media unsupported for conversation type %q", ident.ConversationType)
	}
}
```

Update dispatch and interaction send sites:

```go
if err := b.SendResponse(ctx, message, reply); err != nil {
	b.recordOutbound(ctx, message.Identity, reply.Text, store.InteractionStatusFailed, err)
	b.logf("send QQ bot reply failed: %v", err)
	return
}
```

Change `handleMessage` to return `commands.Response` in the same pattern as NapCat, wrapping agent text as `commands.Response{Text: reply, Kind: "agent"}`.

- [ ] **Step 4: Run QQ tests**

Run:

```bash
go test ./internal/qqbot -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/qqbot/bot.go internal/qqbot/bot_test.go
git commit -m "feat(bot): send qq rich media responses"
```

## Task 6: Full Verification, PR, and Production Smoke

**Files:**
- Modify only files required by failing verification from Tasks 1-5.

**Interfaces:**
- Consumes: all previous tasks.
- Produces: a verified branch, PR, merged code, deployed production smoke result.

- [ ] **Step 1: Run full Go tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Build the binary**

Run:

```bash
go build ./cmd/life-ustc-bot
```

Expected: PASS and no compiler output.

- [ ] **Step 3: Build Docker image**

Run:

```bash
docker build -t life-ustc-bot:image-responses .
```

Expected: PASS and the runtime stage installs `fonts-noto-cjk`.

- [ ] **Step 4: Inspect git diff**

Run:

```bash
git status --short --branch
git diff --stat origin/main...HEAD
```

Expected: branch contains only docs plus bot rich-image implementation commits.

- [ ] **Step 5: Open PR**

Use the GitHub PR workflow. Before pushing, check review comments if an existing PR is reused:

```bash
gh pr status
git push origin HEAD
gh pr create --fill
```

Expected: PR opens against the correct repository branch.

- [ ] **Step 6: Watch GitHub Actions**

Run:

```bash
gh pr checks --watch
```

Expected: all required checks pass. If a check fails, inspect logs, fix with a new failing local test when the failure is behavior-related, push again, and watch checks again.

- [ ] **Step 7: Rebase merge**

After review and green checks:

```bash
gh pr merge --rebase --delete-branch
```

Expected: PR is merged and remote feature branch is deleted.

- [ ] **Step 8: Configure production media route**

On the production host, configure the bot container with:

```text
BOT_ENABLE_IMAGE_RESPONSES=true
BOT_PUBLIC_BASE_URL=https://bot.tiankaima.cn
BOT_MEDIA_ADDR=127.0.0.1:2281
BOT_MEDIA_TTL_SECONDS=300
```

Add a Caddy route for:

```text
/media/*
```

to proxy to:

```text
127.0.0.1:2281
```

Do not put Bearer authentication on `/media/*`; the image IDs are random and expire quickly, and QQ/NapCat must fetch the URL directly.

- [ ] **Step 9: Deploy and smoke test**

Deploy the merged bot image, then send these private NapCat messages:

```text
今天课表
td
概览
```

Expected:

- Bot logs show media server listening.
- Bot logs show NapCat image segment send, or text fallback with a logged image error.
- `https://bot.tiankaima.cn/media/<id>.png` returns `Content-Type: image/png` while the TTL is active.
- Private chat receives an image for at least one structured response.

---

## Self-Review

- Spec coverage: Tasks 1-5 cover rich command responses, renderer, short-lived media serving, NapCat send, QQ official upload/send, and text fallback. Task 6 covers rollout, PR checks, deploy, and smoke.
- Scope choice: free-form agent and Markdown remain text-only because the spec marks them non-goals and prior Markdown testing failed in this environment.
- Type consistency: `commands.Response` carries `*responses.Image`; platform adapters call `SendResponse`; login and notification senders remain string-only.
