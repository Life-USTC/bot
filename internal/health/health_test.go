package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeStore struct {
	err error
}

func (s fakeStore) Ping(context.Context) error { return s.err }

func TestLive(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "sqlite available", status: http.StatusOK},
		{name: "sqlite unavailable", err: errors.New("closed"), status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/live", nil)
			res := httptest.NewRecorder()
			NewHandler(fakeStore{err: test.err}).ServeHTTP(res, req)
			if res.Code != test.status {
				t.Fatalf("status = %d, want %d", res.Code, test.status)
			}
		})
	}
}

func TestLiveRejectsPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/live", nil)
	res := httptest.NewRecorder()
	NewHandler(fakeStore{}).ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}
