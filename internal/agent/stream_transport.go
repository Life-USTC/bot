package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

func isEventStream(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mediaType == "text/event-stream"
}

// Observe SSE frames while the model SDK consumes them. Retain at most the
// current event, preserve wire bytes, and require the provider's completion
// marker so an interrupted stream cannot look like a complete answer.
type usageStreamBody struct {
	body                      io.ReadCloser
	reader                    *bufio.Reader
	ctx                       context.Context
	started, bodyStarted      time.Time
	pending, event            []byte
	pendingErr                error
	done, usageSeen, textSeen bool
	closeOnce                 sync.Once
	closeErr                  error
}

func newUsageStreamBody(ctx context.Context, body io.ReadCloser, started time.Time) *usageStreamBody {
	return &usageStreamBody{body: body, reader: bufio.NewReader(body), ctx: ctx, started: started, bodyStarted: time.Now()}
}

func (s *usageStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		if s.pendingErr != nil {
			return 0, s.pendingErr
		}
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			s.pending = line
			s.observe(line)
		}
		if err != nil {
			if err == io.EOF && !s.done {
				err = io.ErrUnexpectedEOF
			}
			s.pendingErr = err
			if len(line) == 0 {
				return 0, err
			}
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *usageStreamBody) observe(line []byte) {
	line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
	if len(line) != 0 {
		if data, found := bytes.CutPrefix(line, []byte("data:")); found {
			data = bytes.TrimPrefix(data, []byte(" "))
			if len(s.event) > 0 {
				s.event = append(s.event, '\n')
			}
			s.event = append(s.event, data...)
		}
		return
	}
	defer func() { s.event = s.event[:0] }()
	if strings.TrimSpace(string(s.event)) == "[DONE]" {
		s.done = true
		return
	}
	var event struct {
		Usage   json.RawMessage `json:"usage"`
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(s.event, &event) != nil {
		return
	}
	if !s.usageSeen && len(event.Usage) > 0 && string(event.Usage) != "null" {
		captureProviderUsage(s.ctx, s.event)
		s.usageSeen = true
	}
	if !s.textSeen {
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				recordRunStage(s.ctx, "model_first_text", time.Since(s.started))
				s.textSeen = true
				break
			}
		}
	}
}

func (s *usageStreamBody) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.body.Close()
		recordRunStage(s.ctx, "model_response_body", time.Since(s.bodyStarted))
	})
	return s.closeErr
}

type timedStreamBody struct {
	io.ReadCloser
	ctx     context.Context
	started time.Time
	once    sync.Once
}

func (b *timedStreamBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { recordRunStage(b.ctx, "model_request", time.Since(b.started)) })
	return err
}
