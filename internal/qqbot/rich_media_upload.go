package qqbot

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Life-USTC/Bot/internal/store"
)

const (
	qqRichMediaFileName          = "image.png"
	qqRichMediaMD510MBytes       = 10002432
	qqRichMediaMaxPartAttempts   = 3
	qqRichMediaDefaultRetryLimit = 5 * time.Minute
)

var errRichMediaRetryTimeout = errors.New("qq bot rich media retry timeout")

type richMediaPrepareRequest struct {
	FileType int    `json:"file_type"`
	FileSize string `json:"file_size"`
	FileName string `json:"file_name"`
	MD5      string `json:"md5"`
	SHA1     string `json:"sha1"`
	MD510M   string `json:"md5_10m"`
}

type richMediaPrepareResponse struct {
	UploadID     string                `json:"upload_id"`
	BlockSize    qqFlexibleInt64       `json:"block_size"`
	Parts        []richMediaUploadPart `json:"parts"`
	UploadConfig richMediaUploadConfig `json:"upload_config"`
}

type richMediaUploadPart struct {
	Index        int             `json:"index"`
	PresignedURL string          `json:"presigned_url"`
	BlockSize    qqFlexibleInt64 `json:"block_size"`
	PartSize     qqFlexibleInt64 `json:"part_size"`
}

type richMediaUploadConfig struct {
	Concurrency  int `json:"concurrency"`
	RetryTimeout int `json:"retry_timeout"`
	RetryDelay   int `json:"retry_delay"`
}

type richMediaPartFinishRequest struct {
	UploadID  string `json:"upload_id"`
	PartIndex int    `json:"part_index"`
	BlockSize string `json:"block_size"`
	MD5       string `json:"md5"`
}

// qqFlexibleInt64 accepts the numeric and quoted numeric forms used by the
// QQ upload API for byte counts.
type qqFlexibleInt64 int64

func (v *qqFlexibleInt64) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*v = 0
		return nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fmt.Errorf("parse QQ upload integer %q: %w", value, err)
		}
		*v = qqFlexibleInt64(parsed)
		return nil
	}
	var parsed int64
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*v = qqFlexibleInt64(parsed)
	return nil
}

func (b *Bot) uploadRichMediaBytes(ctx context.Context, ident store.Identity, token string, imageData []byte) (richMediaUploadResponse, error) {
	if len(imageData) == 0 {
		return richMediaUploadResponse{}, preSendError{err: errors.New("qq bot rich media bytes are empty")}
	}
	preparePath, err := richMediaPreparePath(ident)
	if err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	fileMD5, fileSHA1, first10MiBMD5 := richMediaHashes(imageData)
	prepareRequest := richMediaPrepareRequest{
		FileType: 1,
		FileSize: strconv.FormatInt(int64(len(imageData)), 10),
		FileName: qqRichMediaFileName,
		MD5:      fileMD5,
		SHA1:     fileSHA1,
		MD510M:   first10MiBMD5,
	}
	startedAt := time.Now()
	var prepared richMediaPrepareResponse
	if err := b.openAPI(ctx, http.MethodPost, preparePath, token, prepareRequest, &prepared); err != nil {
		b.logf("QQ bot media prepare failed: conversation_type=%q upload_ms=%d error=%v",
			ident.ConversationType, time.Since(startedAt).Milliseconds(), err)
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	if err := validateRichMediaPrepare(prepared, len(imageData)); err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	policy := newRichMediaRetryPolicy(prepared.UploadConfig)
	finishPath, err := richMediaPartFinishPath(ident)
	if err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	for _, part := range prepared.Parts {
		partData, err := richMediaPartData(imageData, prepared.BlockSize, part)
		if err != nil {
			return richMediaUploadResponse{}, preSendError{err: err}
		}
		if err := b.putRichMediaPart(ctx, part.PresignedURL, partData, policy); err != nil {
			return richMediaUploadResponse{}, preSendError{err: err}
		}
		finishRequest := richMediaPartFinishRequest{
			UploadID:  prepared.UploadID,
			PartIndex: part.Index,
			BlockSize: strconv.Itoa(len(partData)),
			MD5:       md5Hex(partData),
		}
		if err := b.finishRichMediaPart(ctx, finishPath, token, finishRequest, policy); err != nil {
			return richMediaUploadResponse{}, preSendError{err: err}
		}
	}

	mergePath, err := richMediaUploadPath(ident)
	if err != nil {
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	var merged richMediaUploadResponse
	if err := b.openAPI(ctx, http.MethodPost, mergePath, token, richMediaUploadRequest{
		FileType:   1,
		SrvSendMsg: false,
		FileName:   qqRichMediaFileName,
		UploadID:   prepared.UploadID,
	}, &merged); err != nil {
		b.logf("QQ bot media merge failed: conversation_type=%q upload_ms=%d error=%v",
			ident.ConversationType, time.Since(startedAt).Milliseconds(), err)
		return richMediaUploadResponse{}, preSendError{err: err}
	}
	if len(merged.FileInfo) == 0 {
		return richMediaUploadResponse{}, preSendError{err: errors.New("qq bot rich media merge missing file_info")}
	}
	b.logf("QQ bot media uploaded: conversation_type=%q ttl_seconds=%d upload_ms=%d",
		ident.ConversationType, merged.TTL, time.Since(startedAt).Milliseconds())
	return merged, nil
}

func richMediaHashes(data []byte) (md5HexValue, sha1HexValue, first10MiBMD5 string) {
	md5Value := md5.Sum(data)
	sha1Value := sha1.Sum(data)
	limit := len(data)
	if limit > qqRichMediaMD510MBytes {
		limit = qqRichMediaMD510MBytes
	}
	first10Value := md5.Sum(data[:limit])
	return hex.EncodeToString(md5Value[:]), hex.EncodeToString(sha1Value[:]), hex.EncodeToString(first10Value[:])
}

func md5Hex(data []byte) string {
	value := md5.Sum(data)
	return hex.EncodeToString(value[:])
}

func validateRichMediaPrepare(prepared richMediaPrepareResponse, dataSize int) error {
	if strings.TrimSpace(prepared.UploadID) == "" {
		return errors.New("qq bot rich media prepare missing upload_id")
	}
	if int64(prepared.BlockSize) <= 0 {
		return errors.New("qq bot rich media prepare has invalid block_size")
	}
	if len(prepared.Parts) == 0 {
		return errors.New("qq bot rich media prepare returned no parts")
	}
	seen := make(map[int]struct{}, len(prepared.Parts))
	for _, part := range prepared.Parts {
		if part.Index < 0 {
			return errors.New("qq bot rich media prepare returned negative part index")
		}
		if _, exists := seen[part.Index]; exists {
			return fmt.Errorf("qq bot rich media prepare returned duplicate part index %d", part.Index)
		}
		seen[part.Index] = struct{}{}
		if strings.TrimSpace(part.PresignedURL) == "" {
			return fmt.Errorf("qq bot rich media prepare part %d has empty presigned_url", part.Index)
		}
		partSize := richMediaPartSize(prepared.BlockSize, part)
		if partSize <= 0 {
			return fmt.Errorf("qq bot rich media prepare part %d has invalid block_size", part.Index)
		}
		if int64(part.Index) > int64(dataSize)/int64(prepared.BlockSize)+1 {
			return fmt.Errorf("qq bot rich media prepare part %d is outside file", part.Index)
		}
		offset := int64(part.Index) * int64(prepared.BlockSize)
		if offset < 0 || offset > int64(dataSize) || partSize > int64(dataSize)-offset {
			return fmt.Errorf("qq bot rich media prepare part %d exceeds file size", part.Index)
		}
	}
	return nil
}

func richMediaPartSize(defaultBlockSize qqFlexibleInt64, part richMediaUploadPart) int64 {
	if part.BlockSize > 0 {
		return int64(part.BlockSize)
	}
	if part.PartSize > 0 {
		return int64(part.PartSize)
	}
	return int64(defaultBlockSize)
}

func richMediaPartData(data []byte, defaultBlockSize qqFlexibleInt64, part richMediaUploadPart) ([]byte, error) {
	partSize := richMediaPartSize(defaultBlockSize, part)
	offset := int64(part.Index) * int64(defaultBlockSize)
	if offset < 0 || offset > int64(len(data)) || partSize <= 0 || partSize > int64(len(data))-offset {
		return nil, fmt.Errorf("qq bot rich media part %d exceeds file size", part.Index)
	}
	return data[int(offset):int(offset+partSize)], nil
}

type richMediaRetryPolicy struct {
	timeout time.Duration
	delay   time.Duration
}

func newRichMediaRetryPolicy(config richMediaUploadConfig) richMediaRetryPolicy {
	timeout := time.Duration(config.RetryTimeout) * time.Second
	if timeout <= 0 {
		timeout = qqRichMediaDefaultRetryLimit
	}
	delay := time.Duration(config.RetryDelay) * time.Second
	if delay < 0 {
		delay = 0
	}
	return richMediaRetryPolicy{timeout: timeout, delay: delay}
}

func (b *Bot) putRichMediaPart(ctx context.Context, presignedURL string, data []byte, policy richMediaRetryPolicy) error {
	deadline := time.Now().Add(policy.timeout)
	var lastErr error
	for attempt := 1; attempt <= qqRichMediaMaxPartAttempts; attempt++ {
		attemptCtx, cancel, err := richMediaAttemptContext(ctx, deadline)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPut, presignedURL, bytes.NewReader(data))
		if err != nil {
			cancel()
			return errors.New("qq bot rich media part PUT request is invalid")
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := b.httpClient().Do(req)
		attemptErr := attemptCtx.Err()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attemptErr == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) || !time.Now().Before(deadline) {
				return errRichMediaRetryTimeout
			}
			if _, ok := err.(net.Error); !ok {
				return errors.New("qq bot rich media part PUT transport failed")
			}
			lastErr = errors.New("qq bot rich media part PUT transport failed")
		} else {
			_ = resp.Body.Close()
			if !time.Now().Before(deadline) {
				return errRichMediaRetryTimeout
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = qqBotHTTPStatusError{method: http.MethodPut, path: "<presigned-url>", status: resp.StatusCode}
			if !isRetryableRichMediaStatus(resp.StatusCode) {
				return lastErr
			}
		}
		if attempt == qqRichMediaMaxPartAttempts {
			return lastErr
		}
		if err := waitRichMediaRetry(ctx, policy.delay, deadline); err != nil {
			return err
		}
	}
	return lastErr
}

func (b *Bot) finishRichMediaPart(ctx context.Context, path, token string, body richMediaPartFinishRequest, policy richMediaRetryPolicy) error {
	deadline := time.Now().Add(policy.timeout)
	var lastErr error
	for attempt := 1; attempt <= qqRichMediaMaxPartAttempts; attempt++ {
		attemptCtx, cancel, err := richMediaAttemptContext(ctx, deadline)
		if err != nil {
			return err
		}
		err = b.openAPI(attemptCtx, http.MethodPost, path, token, body, nil)
		attemptErr := attemptCtx.Err()
		cancel()
		if err == nil && attemptErr == nil && time.Now().Before(deadline) {
			return nil
		}
		if err == nil {
			lastErr = errRichMediaRetryTimeout
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else if attemptErr == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) || !time.Now().Before(deadline) {
			return errRichMediaRetryTimeout
		} else {
			lastErr = err
			if !isRetryableRichMediaPartFinish(err) {
				return err
			}
		}
		if attempt == qqRichMediaMaxPartAttempts {
			return lastErr
		}
		if err := waitRichMediaRetry(ctx, policy.delay, deadline); err != nil {
			return err
		}
	}
	return lastErr
}

func isRetryableRichMediaPartFinish(err error) bool {
	var statusErr qqBotHTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.code == 40093001 || isRetryableRichMediaStatus(statusErr.status)
}

func isRetryableRichMediaStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return status >= 500 && status <= 599
	}
}

func richMediaAttemptContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, nil, errRichMediaRetryTimeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, remaining)
	return attemptCtx, cancel, nil
}

func waitRichMediaRetry(ctx context.Context, delay time.Duration, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return errRichMediaRetryTimeout
	}
	if delay <= 0 {
		return nil
	}
	if delay > remaining {
		delay = remaining
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if !time.Now().Before(deadline) {
			return errRichMediaRetryTimeout
		}
		return nil
	}
}
