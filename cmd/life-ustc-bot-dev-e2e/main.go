package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const testMessage = "能再给我发一下日历的链接吗"

var (
	httpURLPattern  = regexp.MustCompile(`https?://[^\s]+`)
	userCodePattern = regexp.MustCompile(`验证码：([A-Z0-9-]+)`)
)

type options struct {
	botBinary string
	server    string
	runDir    string
}

func main() {
	var opts options
	flag.StringVar(&opts.botBinary, "bot", "", "path to the Bot binary")
	flag.StringVar(&opts.server, "server", "http://localhost:3100", "local Life@USTC server origin")
	flag.StringVar(&opts.runDir, "run-dir", "", "directory for Bot state and logs")
	flag.Parse()
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "dev E2E failed:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if strings.TrimSpace(opts.botBinary) == "" {
		return errors.New("-bot is required")
	}
	serverURL, err := url.Parse(strings.TrimRight(opts.server, "/"))
	if err != nil || serverURL.Scheme == "" || serverURL.Host == "" {
		return fmt.Errorf("invalid -server value %q", opts.server)
	}
	if !isLoopbackHost(serverURL.Hostname()) {
		return fmt.Errorf("-server must use a local loopback host, got %q", serverURL.Hostname())
	}
	if opts.runDir == "" {
		return errors.New("-run-dir is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	napcat, err := newNapCatHarness()
	if err != nil {
		return err
	}
	bot, err := startBot(ctx, opts, napcat.Address())
	if err != nil {
		return err
	}
	defer bot.Stop()
	conn, err := napcat.WaitForConnection(ctx, bot)
	if err != nil {
		return fmt.Errorf("wait for Bot NapCat connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.WriteJSON(privateMessageEvent(testMessage)); err != nil {
		return fmt.Errorf("send user message: %w", err)
	}
	loginReply, err := napcat.WaitForMessage(ctx, conn, func(text string) bool {
		return strings.Contains(text, "需要登录 Life @ USTC")
	})
	if err != nil {
		return fmt.Errorf("wait for login instructions: %w", err)
	}
	verificationURL, userCode, err := loginDetails(loginReply)
	if err != nil {
		return err
	}
	fmt.Println("  received resumable device-login instructions")
	if err := approveDeviceLogin(ctx, serverURL, verificationURL, userCode); err != nil {
		return fmt.Errorf("approve local device login: %w", err)
	}
	fmt.Println("  approved the device grant as the seeded dev user")

	calendarReply, err := napcat.WaitForMessage(ctx, conn, func(text string) bool {
		return strings.Contains(text, "日历订阅链接：") && strings.Contains(text, ".ics")
	})
	if err != nil {
		return fmt.Errorf("wait for resumed calendar-link response: %w", err)
	}
	if strings.Contains(strings.ToLower(calendarReply), "caldav") {
		return errors.New("calendar reply still exposes the obsolete CalDAV wording")
	}
	for _, required := range []string{"iCalendar", "通过 URL", "自动更新", "请勿公开"} {
		if !strings.Contains(calendarReply, required) {
			return fmt.Errorf("calendar reply is missing %q", required)
		}
	}
	calendarURL, err := calendarLink(calendarReply)
	if err != nil {
		return err
	}
	if err := verifyCalendarFeed(ctx, serverURL, calendarURL); err != nil {
		return err
	}
	fmt.Println("  original request resumed without another user command")
	fmt.Println("  private iCalendar URL was delivered and returned a valid VCALENDAR feed")

	if err := conn.WriteJSON(groupMessageEvent(2001, "今天校车好挤")); err != nil {
		return fmt.Errorf("send ambient group message: %w", err)
	}
	if err := conn.WriteJSON(groupMessageEvent(2002, "校车 西区 高新区")); err != nil {
		return fmt.Errorf("send public group query: %w", err)
	}
	publicReply, err := napcat.WaitForActionMessage(ctx, conn, "send_group_msg", func(message napCatMessage) bool {
		return strings.TrimSpace(message.Text) != ""
	})
	if err != nil {
		return fmt.Errorf("wait for public group response: %w", err)
	}
	if publicReply.ReplyTo != "2002" {
		return fmt.Errorf("ambient group text activated Presto: response replied to %q, want public query 2002", publicReply.ReplyTo)
	}
	if err := conn.WriteJSON(groupMessageEvent(2003, "课表")); err != nil {
		return fmt.Errorf("send private group command: %w", err)
	}
	privateRedirect, err := napcat.WaitForActionMessage(ctx, conn, "send_group_msg", func(message napCatMessage) bool {
		return strings.Contains(message.Text, "请私聊 Presto")
	})
	if err != nil {
		return fmt.Errorf("wait for private-chat redirect: %w", err)
	}
	if privateRedirect.ReplyTo != "2003" {
		return fmt.Errorf("private-chat redirect replied to %q, want 2003", privateRedirect.ReplyTo)
	}
	fmt.Println("  ambient group text was ignored; public query and private redirect worked without @")
	return nil
}

type botProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitMu  sync.Mutex
	waitErr error
}

func startBot(ctx context.Context, opts options, napcatAddress string) (*botProcess, error) {
	logPath := filepath.Join(opts.runDir, "bot.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create Bot log: %w", err)
	}
	cmd := exec.CommandContext(ctx, opts.botBinary)
	cmd.Env = environmentWith(map[string]string{
		"LIFE_USTC_SERVER":          strings.TrimRight(opts.server, "/"),
		"BOT_DB_PATH":               filepath.Join(opts.runDir, "bot.db"),
		"BOT_BUILD_VERSION":         "dev-e2e",
		"BOT_HEALTH_ADDR":           "127.0.0.1:0",
		"BOT_ENABLE_NAPCAT_BRIDGE":  "true",
		"NAPCAT_WS_URL":             "",
		"NAPCAT_REVERSE_ADDR":       napcatAddress,
		"NAPCAT_REVERSE_PATH":       "/ws",
		"NAPCAT_API_URL":            "",
		"NAPCAT_ACCESS_TOKEN":       "",
		"BOT_ENABLE_QQ_BOT":         "false",
		"BOT_ENABLE_QQ_BOT_GATEWAY": "false",
		"BOT_ENABLE_QQ_BOT_WEBHOOK": "false",
		"BOT_ENABLE_AGENT":          "false",
		"BOT_HTTP_TIMEOUT_SECONDS":  "10",
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start Bot: %w", err)
	}
	process := &botProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		process.waitMu.Lock()
		process.waitErr = waitErr
		process.waitMu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func environmentWith(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, overridden := overrides[key]; !overridden {
			environment = append(environment, entry)
		}
	}
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	return environment
}

func (p *botProcess) WaitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func (p *botProcess) Stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

type napCatHarness struct {
	address   string
	messageID atomic.Int64
}

func newNapCatHarness() (*napCatHarness, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve NapCat reverse port: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, err
	}
	return &napCatHarness{address: address}, nil
}

func (h *napCatHarness) Address() string {
	return h.address
}

func (h *napCatHarness) WaitForConnection(ctx context.Context, bot *botProcess) (*websocket.Conn, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	endpoint := "ws://" + h.address + "/ws"
	for {
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
		if err == nil {
			return conn, nil
		}
		select {
		case <-bot.done:
			if processErr := bot.WaitError(); processErr != nil {
				return nil, fmt.Errorf("bot stopped before accepting NapCat: %w", processErr)
			}
			return nil, errors.New("bot stopped before accepting NapCat")
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

type napCatAction struct {
	Action string                     `json:"action"`
	Params map[string]json.RawMessage `json:"params"`
	Echo   json.RawMessage            `json:"echo"`
}

type napCatMessage struct {
	Text    string
	ReplyTo string
}

func (h *napCatHarness) WaitForMessage(ctx context.Context, conn *websocket.Conn, accept func(string) bool) (string, error) {
	message, err := h.WaitForActionMessage(ctx, conn, "send_private_msg", func(message napCatMessage) bool {
		return accept(message.Text)
	})
	return message.Text, err
}

func (h *napCatHarness) WaitForActionMessage(ctx context.Context, conn *websocket.Conn, action string, accept func(napCatMessage) bool) (napCatMessage, error) {
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetReadDeadline(deadline)
		}
		var frame napCatAction
		if err := conn.ReadJSON(&frame); err != nil {
			return napCatMessage{}, err
		}
		if err := h.acknowledge(conn, frame); err != nil {
			return napCatMessage{}, err
		}
		if frame.Action != action {
			continue
		}
		message := decodeNapCatMessage(frame.Params["message"])
		if accept(message) {
			return message, nil
		}
	}
}

func (h *napCatHarness) acknowledge(conn *websocket.Conn, frame napCatAction) error {
	data := any([]any{})
	if frame.Action == "send_private_msg" || frame.Action == "send_group_msg" || frame.Action == "send_msg" {
		data = map[string]any{"message_id": h.messageID.Add(1)}
	}
	return conn.WriteJSON(map[string]any{
		"status": "ok", "retcode": 0, "data": data, "echo": frame.Echo,
	})
}

func decodeNapCatMessage(raw json.RawMessage) napCatMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return napCatMessage{Text: text}
	}
	var segments []struct {
		Type string `json:"type"`
		Data struct {
			Text string `json:"text"`
			ID   string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &segments) != nil {
		return napCatMessage{}
	}
	message := napCatMessage{}
	var builder strings.Builder
	for _, segment := range segments {
		switch segment.Type {
		case "reply":
			message.ReplyTo = strings.TrimSpace(segment.Data.ID)
		case "text":
			builder.WriteString(segment.Data.Text)
		}
	}
	message.Text = builder.String()
	return message
}

func privateMessageEvent(text string) map[string]any {
	return map[string]any{
		"time": time.Now().Unix(), "self_id": 10000, "post_type": "message",
		"message_type": "private", "sub_type": "friend", "message_id": 1001,
		"user_id": 42, "message": text, "raw_message": text, "font": 0,
		"sender": map[string]any{"user_id": 42, "nickname": "Dev User"},
	}
}

func groupMessageEvent(messageID int64, text string) map[string]any {
	return map[string]any{
		"time": time.Now().Unix(), "self_id": 10000, "post_type": "message",
		"message_type": "group", "sub_type": "normal", "message_id": messageID,
		"user_id": 42, "group_id": 100, "message": text, "raw_message": text, "font": 0,
		"sender": map[string]any{"user_id": 42, "nickname": "Dev User"},
	}
}

func loginDetails(reply string) (*url.URL, string, error) {
	var verificationURL *url.URL
	for _, candidate := range httpURLPattern.FindAllString(reply, -1) {
		parsed, err := url.Parse(strings.TrimRight(candidate, "。,.，)）"))
		if err == nil && parsed.Path == "/oauth/device" {
			verificationURL = parsed
			break
		}
	}
	match := userCodePattern.FindStringSubmatch(reply)
	if verificationURL == nil || len(match) != 2 {
		return nil, "", fmt.Errorf("invalid login instructions: %q", reply)
	}
	return verificationURL, match[1], nil
}

func approveDeviceLogin(ctx context.Context, serverURL, verificationURL *url.URL, userCode string) error {
	if verificationURL.Scheme != serverURL.Scheme || verificationURL.Host != serverURL.Host {
		return fmt.Errorf("verification URL %s does not belong to local server %s", verificationURL.Redacted(), serverURL.Redacted())
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	callback := verificationURL.RequestURI()
	if _, err := postForm(ctx, client, serverURL.String()+"/account/sign-in", serverURL.String(), url.Values{
		"providerId": {"dev-debug"}, "callbackUrl": {callback},
	}); err != nil {
		return fmt.Errorf("debug sign-in: %w", err)
	}
	sessionRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL.String()+"/api/auth/get-session", nil)
	if err != nil {
		return err
	}
	sessionResponse, err := client.Do(sessionRequest)
	if err != nil {
		return err
	}
	sessionBody, readErr := io.ReadAll(io.LimitReader(sessionResponse.Body, 64<<10))
	_ = sessionResponse.Body.Close()
	if readErr != nil {
		return readErr
	}
	if sessionResponse.StatusCode != http.StatusOK || !strings.Contains(string(sessionBody), `"id"`) {
		return fmt.Errorf("debug session was not established: status=%d", sessionResponse.StatusCode)
	}
	approvalURL := serverURL.String() + "/oauth/device?/approve"
	result, err := postForm(ctx, client, approvalURL, serverURL.String(), url.Values{"userCode": {userCode}})
	if err != nil {
		return err
	}
	var actionResult struct {
		Type     string `json:"type"`
		Location string `json:"location"`
	}
	if json.Unmarshal(result.body, &actionResult) == nil && actionResult.Type == "redirect" {
		location, parseErr := url.Parse(actionResult.Location)
		if parseErr == nil && location.Query().Get("result") == "approved" {
			return nil
		}
	}
	response := result.response
	if response.Request == nil || response.Request.URL == nil {
		return errors.New("device approval response has no final URL")
	}
	if response.Request.URL.Query().Get("result") != "approved" {
		return fmt.Errorf("device approval ended at %s", response.Request.URL.Redacted())
	}
	return nil
}

type formResponse struct {
	response *http.Response
	body     []byte
}

func postForm(ctx context.Context, client *http.Client, endpoint, origin string, form url.Values) (formResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return formResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	resp, err := client.Do(req)
	if err != nil {
		return formResponse{}, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return formResponse{}, readErr
	}
	if closeErr != nil {
		return formResponse{}, closeErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return formResponse{}, fmt.Errorf("%s returned %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	return formResponse{response: resp, body: body}, nil
}

func calendarLink(reply string) (*url.URL, error) {
	for _, candidate := range httpURLPattern.FindAllString(reply, -1) {
		parsed, err := url.Parse(strings.TrimRight(candidate, "。,.，)）"))
		if err == nil && strings.HasSuffix(parsed.Path, ".ics") {
			return parsed, nil
		}
	}
	return nil, errors.New("calendar reply does not contain an iCalendar URL")
}

func verifyCalendarFeed(ctx context.Context, serverURL, calendarURL *url.URL) error {
	if calendarURL.Scheme != serverURL.Scheme || calendarURL.Host != serverURL.Host {
		return fmt.Errorf("calendar URL %s does not belong to local server %s", calendarURL.Redacted(), serverURL.Redacted())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, calendarURL.String(), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch iCalendar feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("iCalendar feed returned %s", resp.Status)
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/calendar") {
		return fmt.Errorf("iCalendar feed content type = %q", resp.Header.Get("Content-Type"))
	}
	text := string(body)
	if !strings.Contains(text, "BEGIN:VCALENDAR") || !strings.Contains(text, "END:VCALENDAR") {
		return errors.New("iCalendar feed body is not a VCALENDAR document")
	}
	return nil
}
