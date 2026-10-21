// copilot-extauth is an Envoy ext_authz (gRPC) server that lets each user reach GitHub Copilot
// through Agent Router with their own GitHub identity.
//
// For every request it:
//  1. reads the user's GitHub token (x-github-token header, or Authorization: Bearer/token ...),
//  2. optionally checks the user is an active member of ALLOWED_ORG,
//  3. exchanges the GitHub token for a short-lived Copilot token, or passes it through when the
//     exchange is refused (TOKEN_MODE=auto, default),
//  4. returns the Copilot token as ext_authz dynamic metadata, which the BackendSecurityPolicy
//     credentialOverride.fromDynamicMetadata picks up and injects as the upstream Authorization.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type config struct {
	port            string
	githubAPI       string
	tokenHeader     string
	mode            string // "auto", "exchange" or "passthrough"
	allowedOrg      string
	metadataKey     string
	integrationID   string
	editorVersion   string
	failureCacheTTL time.Duration
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		port:            env("PORT", "9001"),
		githubAPI:       strings.TrimRight(env("GITHUB_API", "https://api.github.com"), "/"),
		tokenHeader:     strings.ToLower(env("GITHUB_TOKEN_HEADER", "x-github-token")),
		mode:            env("TOKEN_MODE", "auto"),
		allowedOrg:      os.Getenv("ALLOWED_ORG"),
		metadataKey:     env("METADATA_KEY", "x-aigw-api-key"),
		integrationID:   env("COPILOT_INTEGRATION_ID", "vscode-chat"),
		editorVersion:   env("EDITOR_VERSION", "vscode/1.99.0"),
		failureCacheTTL: 30 * time.Second,
	}
}

type cacheEntry struct {
	value string // copilot token; empty means "denied"
	err   string
	exp   time.Time
}

type server struct {
	authv3.UnimplementedAuthorizationServer
	cfg  config
	http *http.Client
	mu   sync.Mutex
	// keyed by sha256(github token) so raw tokens are never held as map keys
	cache map[string]cacheEntry
}

func newServer(cfg config) *server {
	s := &server{cfg: cfg, http: &http.Client{Timeout: 10 * time.Second}, cache: map[string]cacheEntry{}}
	go s.janitor()
	return s
}

func (s *server) janitor() {
	for range time.Tick(time.Minute) {
		now := time.Now()
		s.mu.Lock()
		for k, e := range s.cache {
			if now.After(e.exp) {
				delete(s.cache, k)
			}
		}
		s.mu.Unlock()
	}
}

// extractToken prefers the dedicated header, then falls back to Authorization.
func (s *server) extractToken(h map[string]string) string {
	if v := strings.TrimSpace(h[s.cfg.tokenHeader]); v != "" {
		return v
	}
	a := strings.TrimSpace(h["authorization"])
	for _, p := range []string{"Bearer ", "bearer ", "token ", "Token "} {
		if strings.HasPrefix(a, p) {
			return strings.TrimSpace(a[len(p):])
		}
	}
	return ""
}

func (s *server) githubGET(ctx context.Context, path, ghToken string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.githubAPI+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "token "+ghToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "agent-router-copilot-extauth")
	req.Header.Set("Editor-Version", s.cfg.editorVersion)
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 || out == nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func (s *server) checkOrg(ctx context.Context, ghToken string) error {
	if s.cfg.allowedOrg == "" {
		return nil
	}
	var m struct {
		State string `json:"state"`
	}
	code, err := s.githubGET(ctx, "/user/memberships/orgs/"+s.cfg.allowedOrg, ghToken, &m)
	if err != nil {
		return fmt.Errorf("org membership lookup failed: %w", err)
	}
	if code != http.StatusOK || m.State != "active" {
		return fmt.Errorf("user is not an active member of %s (status %d)", s.cfg.allowedOrg, code)
	}
	return nil
}

// resolve returns the upstream Copilot credential for a GitHub token, using the cache.
func (s *server) resolve(ctx context.Context, ghToken string) (string, error) {
	sum := sha256.Sum256([]byte(ghToken))
	key := hex.EncodeToString(sum[:])

	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Now().Before(e.exp) {
		s.mu.Unlock()
		if e.value == "" {
			return "", fmt.Errorf("%s", e.err)
		}
		return e.value, nil
	}
	s.mu.Unlock()

	val, exp, err := s.fetch(ctx, ghToken)

	s.mu.Lock()
	if err != nil {
		s.cache[key] = cacheEntry{err: err.Error(), exp: time.Now().Add(s.cfg.failureCacheTTL)}
	} else {
		s.cache[key] = cacheEntry{value: val, exp: exp}
	}
	s.mu.Unlock()
	return val, err
}

func (s *server) fetch(ctx context.Context, ghToken string) (string, time.Time, error) {
	if err := s.checkOrg(ctx, ghToken); err != nil {
		return "", time.Time{}, err
	}
	switch s.cfg.mode {
	case "passthrough":
		return s.passthrough(ctx, ghToken)
	case "exchange":
		return s.exchange(ctx, ghToken)
	default: // auto
		if tok, exp, err := s.exchange(ctx, ghToken); err == nil {
			return tok, exp, nil
		}
		// Tokens from OAuth apps other than Copilot's own (e.g. `gh auth token`) are refused by the
		// exchange but accepted directly by api.githubcopilot.com.
		return s.passthrough(ctx, ghToken)
	}
}

// passthrough validates the GitHub token once and forwards it unchanged.
func (s *server) passthrough(ctx context.Context, ghToken string) (string, time.Time, error) {
	code, err := s.githubGET(ctx, "/user", ghToken, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	if code != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("invalid GitHub token (status %d)", code)
	}
	return ghToken, time.Now().Add(5 * time.Minute), nil
}

// exchange trades the GitHub token for a short-lived Copilot session token.
func (s *server) exchange(ctx context.Context, ghToken string) (string, time.Time, error) {
	var t struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	code, err := s.githubGET(ctx, "/copilot_internal/v2/token", ghToken, &t)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token exchange failed: %w", err)
	}
	if code != http.StatusOK || t.Token == "" {
		return "", time.Time{}, fmt.Errorf("copilot token exchange denied (status %d): no Copilot access for this token", code)
	}
	exp := time.Unix(t.ExpiresAt, 0).Add(-2 * time.Minute) // refresh before Copilot expiry
	if t.ExpiresAt == 0 || time.Until(exp) < time.Minute {
		exp = time.Now().Add(time.Minute)
	}
	return t.Token, exp, nil
}

func deny(code codes.Code, httpCode typev3.StatusCode, msg string) *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(code), Message: msg},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{DeniedResponse: &authv3.DeniedHttpResponse{
			Status:  &typev3.HttpStatus{Code: httpCode},
			Headers: []*corev3.HeaderValueOption{hdr("content-type", "application/json")},
			Body:    fmt.Sprintf(`{"error":{"message":%q,"type":"authentication_error"}}`, msg),
		}},
	}
}

func hdr(k, v string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		Header:       &corev3.HeaderValue{Key: k, Value: v},
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
	}
}

func (s *server) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	h := req.GetAttributes().GetRequest().GetHttp().GetHeaders()
	ghToken := s.extractToken(h)
	if ghToken == "" {
		return deny(codes.Unauthenticated, typev3.StatusCode_Unauthorized,
			"missing GitHub token: send it as '"+s.cfg.tokenHeader+"' or 'Authorization: Bearer <token>'"), nil
	}

	cred, err := s.resolve(ctx, ghToken)
	if err != nil {
		log.Printf("deny: %v", err)
		return deny(codes.PermissionDenied, typev3.StatusCode_Forbidden, err.Error()), nil
	}

	md, _ := structpb.NewStruct(map[string]any{s.cfg.metadataKey: cred})
	return &authv3.CheckResponse{
		Status:          &status.Status{Code: int32(codes.OK)},
		DynamicMetadata: md,
		HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{
			// Copilot's API requires these; set them so any OpenAI-compatible client works.
			Headers: []*corev3.HeaderValueOption{
				hdr("copilot-integration-id", s.cfg.integrationID),
				hdr("editor-version", s.cfg.editorVersion),
			},
			// The GitHub token must never travel further than this service.
			HeadersToRemove: []string{s.cfg.tokenHeader},
		}},
	}, nil
}

func main() {
	cfg := loadConfig()
	if cfg.mode != "auto" && cfg.mode != "exchange" && cfg.mode != "passthrough" {
		log.Fatalf("TOKEN_MODE must be 'auto', 'exchange' or 'passthrough', got %q", cfg.mode)
	}
	lis, err := net.Listen("tcp", ":"+cfg.port)
	if err != nil {
		log.Fatal(err)
	}
	g := grpc.NewServer()
	authv3.RegisterAuthorizationServer(g, newServer(cfg))
	healthpb.RegisterHealthServer(g, health.NewServer())
	log.Printf("copilot-extauth listening on :%s (mode=%s, org=%q)", cfg.port, cfg.mode, cfg.allowedOrg)
	log.Fatal(g.Serve(lis))
}
