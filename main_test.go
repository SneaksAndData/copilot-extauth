package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc/codes"
)

func fakeGitHub(t *testing.T, exchanges *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			atomic.AddInt32(exchanges, 1)
			if auth != "token gho_good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"tid=copilot-session","expires_at":` +
				strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10) + `}`))
		case "/user":
			if auth == "token gho_good" || auth == "token gho_cli" {
				_, _ = w.Write([]byte(`{"login":"u"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/user/memberships/orgs/acme":
			if auth == "token gho_good" || auth == "token gho_cli" {
				_, _ = w.Write([]byte(`{"state":"active"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func check(s *server, headers map[string]string) *authv3.CheckResponse {
	r, _ := s.Check(context.Background(), &authv3.CheckRequest{Attributes: &authv3.AttributeContext{
		Request: &authv3.AttributeContext_Request{Http: &authv3.AttributeContext_HttpRequest{Headers: headers}},
	}})
	return r
}

func TestCheck(t *testing.T) {
	var n int32
	gh := fakeGitHub(t, &n)
	defer gh.Close()
	cfg := loadConfig()
	cfg.githubAPI = gh.URL
	cfg.allowedOrg = "acme"
	s := newServer(cfg)

	// Header token -> exchanged Copilot token in dynamic metadata, GitHub header stripped.
	r := check(s, map[string]string{"x-github-token": "gho_good"})
	if r.Status.Code != int32(codes.OK) {
		t.Fatalf("expected OK, got %v", r.Status)
	}
	if got := r.DynamicMetadata.Fields["x-aigw-api-key"].GetStringValue(); got != "tid=copilot-session" {
		t.Fatalf("metadata = %q", got)
	}
	if rm := r.GetOkResponse().HeadersToRemove; len(rm) != 1 || rm[0] != "x-github-token" {
		t.Fatalf("headers to remove = %v", rm)
	}

	// Authorization: Bearer works too and hits the cache.
	r = check(s, map[string]string{"authorization": "Bearer gho_good"})
	if r.Status.Code != int32(codes.OK) || atomic.LoadInt32(&n) != 1 {
		t.Fatalf("expected cached OK, status=%v exchanges=%d", r.Status, n)
	}

	// Missing token -> 401.
	if r = check(s, map[string]string{}); r.Status.Code != int32(codes.Unauthenticated) {
		t.Fatalf("expected Unauthenticated, got %v", r.Status)
	}

	// auto mode: exchange refused (e.g. `gh auth token`) -> GitHub token passed through.
	r = check(s, map[string]string{"authorization": "Bearer gho_cli"})
	if got := r.DynamicMetadata.GetFields()["x-aigw-api-key"].GetStringValue(); r.Status.Code != int32(codes.OK) || got != "gho_cli" {
		t.Fatalf("expected passthrough, status=%v metadata=%q", r.Status, got)
	}

	// Non-member / no Copilot -> 403.
	if r = check(s, map[string]string{"x-github-token": "gho_bad"}); r.Status.Code != int32(codes.PermissionDenied) {
		t.Fatalf("expected PermissionDenied, got %v", r.Status)
	}
}
