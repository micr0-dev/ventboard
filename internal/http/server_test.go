package boardhttp

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ventboard/internal/classifier"
	"ventboard/internal/db"
	"ventboard/internal/posts"
	"ventboard/internal/rate_limit"
)

type stubCategorizer struct {
	labels []string
	err    error
}

func (s stubCategorizer) Categorize(context.Context, string) ([]string, error) {
	return s.labels, s.err
}

func (s stubCategorizer) Version() string {
	return "stub-model"
}

type sequenceCategorizer struct {
	mu      sync.Mutex
	results [][]string
	index   int
}

func (s *sequenceCategorizer) Categorize(context.Context, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.results) == 0 {
		return nil, nil
	}

	if s.index >= len(s.results) {
		return s.results[len(s.results)-1], nil
	}

	result := s.results[s.index]
	s.index++
	return result, nil
}

func (s *sequenceCategorizer) Version() string {
	return "stub-model"
}

func TestPendingPostsStayHiddenUntilClassified(t *testing.T) {
	t.Parallel()

	server, repo, worker := newTestServer(t, stubCategorizer{labels: []string{"grief"}})

	submitPost(t, server, "rough night")

	before := httptest.NewRequest(http.MethodGet, "/", nil)
	beforeRec := httptest.NewRecorder()
	server.ServeHTTP(beforeRec, before)

	if strings.Contains(beforeRec.Body.String(), "rough night") {
		t.Fatal("pending post should not be visible before classification")
	}

	if _, err := worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext returned error: %v", err)
	}

	after := httptest.NewRequest(http.MethodGet, "/", nil)
	afterRec := httptest.NewRecorder()
	server.ServeHTTP(afterRec, after)

	body := afterRec.Body.String()
	if !strings.Contains(body, "CW: grief") {
		t.Fatalf("expected grief CW in response body, got %q", body)
	}
	if !strings.Contains(body, "rough night") {
		t.Fatal("expected published post body to be visible")
	}

	items, err := repo.ListPublished(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPublished returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 published post, got %d", len(items))
	}
}

func TestSensitivePostsRenderInDetailsElement(t *testing.T) {
	t.Parallel()

	server, _, worker := newTestServer(t, stubCategorizer{labels: []string{"violence", "grief"}})

	submitPost(t, server, "content with warnings")
	if _, err := worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "<details") {
		t.Fatal("expected details element for CW post")
	}
	if !strings.Contains(body, "CW: violence, grief") {
		t.Fatalf("expected joined CW labels, got %q", body)
	}
}

func TestConsecutiveSpamPostsCollapseIntoOneBlock(t *testing.T) {
	t.Parallel()

	categorizer := &sequenceCategorizer{
		results: [][]string{
			{"spam"},
			{"spam", "grief"},
			nil,
		},
	}

	server, _, worker := newTestServer(t, categorizer)

	submitPost(t, server, "buy this now")
	submitPost(t, server, "click this link")
	submitPost(t, server, "real post")

	for range 3 {
		if _, err := worker.ProcessNext(context.Background()); err != nil {
			t.Fatalf("ProcessNext returned error: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "2 spam posts collapsed") {
		t.Fatalf("expected collapsed spam summary, got %q", body)
	}
	if !strings.Contains(body, "buy this now") || !strings.Contains(body, "click this link") {
		t.Fatal("expected spam posts to remain in the rendered HTML")
	}
	if !strings.Contains(body, "real post") {
		t.Fatal("expected non-spam post to render outside spam block")
	}
}

func TestSingleSpamPostStaysVisibleButMuted(t *testing.T) {
	t.Parallel()

	server, _, worker := newTestServer(t, stubCategorizer{labels: []string{"spam"}})

	submitPost(t, server, "cheap pills")
	if _, err := worker.ProcessNext(context.Background()); err != nil {
		t.Fatalf("ProcessNext returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "spam posts collapsed") {
		t.Fatal("single spam post should not collapse into a group")
	}
	if !strings.Contains(body, `class="panel post post-spam"`) {
		t.Fatalf("expected spam post styling, got %q", body)
	}
	if !strings.Contains(body, "CW: spam") {
		t.Fatalf("expected spam label, got %q", body)
	}
}

func TestHealthEndpointReturnsOK(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("unexpected health body: %q", string(body))
	}
}

func TestRobotsTxtDisallowsIndexing(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "User-agent: *") || !strings.Contains(body, "Disallow: /") {
		t.Fatalf("unexpected robots body: %q", body)
	}
}

func TestSecurityHeadersPresent(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow, noarchive" {
		t.Fatalf("unexpected X-Robots-Tag: %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("unexpected X-Content-Type-Options: %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("unexpected X-Frame-Options: %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("unexpected CSP: %q", got)
	}
}

func TestStaticAssetsHaveCacheControl(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})
	req := httptest.NewRequest(http.MethodGet, "/static/site.css", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=604800, immutable" {
		t.Fatalf("unexpected Cache-Control: %q", got)
	}
}

func TestHoneypotPostIsSilentlyDropped(t *testing.T) {
	t.Parallel()

	server, repo, _ := newTestServer(t, stubCategorizer{})

	form := url.Values{}
	form.Set("body", "bot garbage")
	form.Set("form_token", server.formProtector.IssueToken())
	form.Set(honeypotFieldName, "https://spam.example")

	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "8.8.8.8:1234"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}

	items, err := repo.ListPublished(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListPublished returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no published posts, got %d", len(items))
	}
}

func TestFreshFormTokenRejected(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})
	server.formProtector.minAge = time.Minute
	server.formProtector.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }

	form := url.Values{}
	form.Set("body", "too fast")
	form.Set("form_token", server.formProtector.IssueToken())

	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "8.8.8.8:1234"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too fast") {
		t.Fatal("expected draft body to be preserved on token error")
	}
}

func TestValidationErrorPreservesDraftBody(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t, stubCategorizer{})

	secondForm := url.Values{}
	secondForm.Set("body", strings.Repeat("x", 2001))
	secondForm.Set("form_token", server.formProtector.IssueToken())

	secondReq := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(secondForm.Encode()))
	secondReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secondReq.RemoteAddr = "8.8.8.8:1234"
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)

	if secondRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", secondRec.Code)
	}
	if !strings.Contains(secondRec.Body.String(), strings.Repeat("x", 2001)) {
		t.Fatal("expected draft body to be preserved on validation error")
	}
}

func newTestServer(t *testing.T, categorizer classifier.Categorizer) (*Server, *posts.Repository, *classifier.Worker) {
	t.Helper()

	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(t.Name()))
	database, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open returned error: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
	})

	repo := posts.NewRepository(database)
	limiter := rate_limit.NewLimiter(0, time.Hour, 20, "salt", time.Hour)
	service := posts.NewService(repo, limiter, 2000, categorizer.Version())
	formProtector := NewFormProtector("form-secret", 0, time.Hour)

	server, err := NewServer(database, service, repo, formProtector, "", 100, 2000)
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}

	worker := classifier.NewWorker(repo, categorizer, time.Millisecond, 3, log.New(io.Discard, "", 0))
	return server, repo, worker
}

func submitPost(t *testing.T, server *Server, body string) {
	t.Helper()

	form := url.Values{}
	form.Set("body", body)
	form.Set("form_token", server.formProtector.IssueToken())
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "8.8.8.8:1234"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
}
