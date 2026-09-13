package qqbot

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Life-USTC/Bot/internal/message"
	"github.com/Life-USTC/Bot/internal/store"
)

func TestRichMediaUploadUsesChunkProtocolHashesAndBoundedFinishRetry(t *testing.T) {
	data := []byte("abcdefghij")
	wantMD5 := md5.Sum(data)
	wantSHA1 := sha1.Sum(data)
	var prepared richMediaPrepareRequest
	parts := map[int][]byte{}
	finishCalls := map[int]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/users/u-1/upload_prepare" {
			if r.Header.Get("Authorization") != "QQBot token" {
				t.Fatalf("prepare authorization = %q", r.Header.Get("Authorization"))
			}
			if err := json.NewDecoder(r.Body).Decode(&prepared); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upload_id":  "upload-1",
				"block_size": "4",
				"parts": []map[string]any{
					{"index": 0, "presigned_url": server.URL + "/part/0", "block_size": "4"},
					{"index": 1, "presigned_url": server.URL + "/part/1", "block_size": "4"},
					{"index": 2, "presigned_url": server.URL + "/part/2", "block_size": "2"},
				},
				"upload_config": map[string]any{"concurrency": 1, "retry_timeout": 1, "retry_delay": 0},
			})
			return
		}
		if len(r.URL.Path) == len("/part/0") && r.URL.Path[:len("/part/")] == "/part/" {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned PUT sent authorization %q", got)
			}
			index, err := strconv.Atoi(r.URL.Path[len("/part/"):])
			if err != nil {
				t.Fatal(err)
			}
			parts[index], err = io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		switch r.URL.Path {
		case "/v2/users/u-1/upload_part_finish":
			var body richMediaPartFinishRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			finishCalls[body.PartIndex]++
			if body.BlockSize != strconv.Itoa(len(parts[body.PartIndex])) || body.MD5 != md5Hex(parts[body.PartIndex]) {
				t.Fatalf("finish body = %#v", body)
			}
			if body.PartIndex == 0 && finishCalls[0] == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":40093001,"message":"retry part finish"}`))
				return
			}
		case "/v2/users/u-1/files":
			var body richMediaUploadRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.UploadID != "upload-1" || body.FileName != qqRichMediaFileName || body.FileType != 1 || body.SrvSendMsg {
				t.Fatalf("merge body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_info": "file-info", "ttl": 120})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ident := store.Identity{Platform: "qqbot", ConversationType: "private", ConversationID: "u-1"}
	uploaded, err := (&Bot{BotToken: "token", APIBaseURL: server.URL, HTTPClient: server.Client()}).uploadRichMedia(context.Background(), ident, data)
	if err != nil {
		t.Fatal(err)
	}
	if string(uploaded.FileInfo) != `"file-info"` || uploaded.TTL != 120 {
		t.Fatalf("uploaded = %#v", uploaded)
	}
	if prepared.FileSize != "10" || prepared.MD5 != hex.EncodeToString(wantMD5[:]) || prepared.SHA1 != hex.EncodeToString(wantSHA1[:]) || prepared.MD510M != prepared.MD5 {
		t.Fatalf("prepare = %#v", prepared)
	}
	if !slices.Equal(parts[0], data[:4]) || !slices.Equal(parts[1], data[4:8]) || !slices.Equal(parts[2], data[8:]) {
		t.Fatalf("parts = %#v", parts)
	}
	if finishCalls[0] != 2 || finishCalls[1] != 1 || finishCalls[2] != 1 {
		t.Fatalf("finish calls = %#v", finishCalls)
	}
}

func TestRichMediaHashesUseOfficialMD510MWindow(t *testing.T) {
	data := make([]byte, qqRichMediaMD510MBytes+1)
	data[qqRichMediaMD510MBytes] = 1
	_, _, got := richMediaHashes(data)
	firstWindow := md5.Sum(data[:qqRichMediaMD510MBytes])
	if got != hex.EncodeToString(firstWindow[:]) {
		t.Fatalf("md5_10m = %q, want first %d bytes", got, qqRichMediaMD510MBytes)
	}
}

func TestQQMediaCacheKeyIncludesTargetScope(t *testing.T) {
	data := []byte("same image")
	one, err := qqMediaCacheKey(store.Identity{ConversationType: "private", ConversationID: "u-1"}, data)
	if err != nil {
		t.Fatal(err)
	}
	two, err := qqMediaCacheKey(store.Identity{ConversationType: "private", ConversationID: "u-2"}, data)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatalf("cache keys collide across targets: %q", one)
	}
}

func TestRichMediaRetryDeadlineBoundsPUTAndDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	bot := &Bot{HTTPClient: server.Client()}
	startedAt := time.Now()
	err := bot.putRichMediaPart(context.Background(), server.URL, []byte("x"), richMediaRetryPolicy{
		timeout: 25 * time.Millisecond,
		delay:   time.Second,
	})
	if !errors.Is(err, errRichMediaRetryTimeout) {
		t.Fatalf("PUT error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("PUT exceeded retry deadline: %s", elapsed)
	}
	startedAt = time.Now()
	err = waitRichMediaRetry(context.Background(), time.Second, time.Now().Add(25*time.Millisecond))
	if !errors.Is(err, errRichMediaRetryTimeout) {
		t.Fatalf("delay error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("retry delay exceeded deadline: %s", elapsed)
	}
}

func TestRichMediaRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&Bot{}).putRichMediaPart(ctx, "http://127.0.0.1:1", []byte("x"), richMediaRetryPolicy{
		timeout: time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled PUT error = %v", err)
	}
}

func TestDeliveryAdapterRejectsRichMediaForChannel(t *testing.T) {
	outcome := NewDeliveryAdapter(&Bot{BotToken: "token"}).Deliver(context.Background(), message.Outbound{
		Target:  message.Conversation{Platform: "qqbot", Type: "channel", ID: "channel-1"},
		Content: message.Content{Attachment: &message.Attachment{MIMEType: "image/png", Data: testPNG(t)}},
	})
	if outcome.Code != "invalid_attachment" {
		t.Fatalf("outcome = %#v", outcome)
	}
}
