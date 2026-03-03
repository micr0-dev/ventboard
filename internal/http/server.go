package boardhttp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"ventboard/internal/posts"
	"ventboard/internal/rate_limit"
	"ventboard/web"
)

const (
	flashErrorCookie  = "ventboard_flash_error"
	flashNoticeCookie = "ventboard_flash_notice"
)

type Server struct {
	db            *sql.DB
	postService   *posts.Service
	repo          *posts.Repository
	formProtector *FormProtector
	sourceURL     string
	templates     *template.Template
	mux           *http.ServeMux
	feedLimit     int
	maxChars      int
}

type indexData struct {
	FlashError  string
	FlashNotice string
	FeedEntries []feedEntry
	FormToken   string
	Honeypot    string
	SourceURL   string
	DraftBody   string
	Autofocus   bool
	MaxChars    int
}

type feedEntry struct {
	Post      postView
	SpamPosts []postView
}

type postView struct {
	Post   posts.Post
	IsSpam bool
}

func NewServer(db *sql.DB, postService *posts.Service, repo *posts.Repository, formProtector *FormProtector, sourceURL string, feedLimit, maxChars int) (*Server, error) {
	tmpl, err := template.New("base.html").Funcs(template.FuncMap{
		"formatTimestamp": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},
		"joinLabels": func(labels []string) string {
			return strings.Join(labels, ", ")
		},
	}).ParseFS(web.Files, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	staticFS, err := fs.Sub(web.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("load static files: %w", err)
	}

	server := &Server{
		db:            db,
		postService:   postService,
		repo:          repo,
		formProtector: formProtector,
		sourceURL:     sourceURL,
		templates:     tmpl,
		mux:           http.NewServeMux(),
		feedLimit:     feedLimit,
		maxChars:      maxChars,
	}

	server.mux.Handle("GET /", http.HandlerFunc(server.handleIndex))
	server.mux.Handle("POST /posts", http.HandlerFunc(server.handleCreatePost))
	server.mux.Handle("GET /healthz", http.HandlerFunc(server.handleHealth))
	server.mux.Handle("GET /robots.txt", http.HandlerFunc(server.handleRobots))
	server.mux.Handle("GET /static/", http.StripPrefix("/static/", withStaticCache(http.FileServer(http.FS(staticFS)))))

	return server, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	applySecurityHeaders(w.Header())
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.renderIndex(w, r, http.StatusOK, indexData{
		FlashError:  readFlash(w, r, flashErrorCookie),
		FlashNotice: readFlash(w, r, flashNoticeCookie),
	})
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request, status int, data indexData) {
	items, err := s.postService.ListPublished(r.Context(), s.feedLimit)
	if err != nil {
		http.Error(w, "could not load board", http.StatusInternalServerError)
		return
	}

	data.FeedEntries = buildFeedEntries(items)
	data.FormToken = s.formProtector.IssueToken()
	data.Honeypot = honeypotFieldName
	data.SourceURL = s.sourceURL
	data.Autofocus = !isLikelyMobileUserAgent(r.UserAgent())
	data.MaxChars = s.maxChars

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
}

func (s *Server) handleCreatePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderIndex(w, r, http.StatusBadRequest, indexData{
			FlashError: "Bad form submission.",
		})
		return
	}
	if strings.TrimSpace(r.FormValue(honeypotFieldName)) != "" {
		setFlash(w, flashNoticeCookie, "Post received. It will appear after CW classification finishes.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	draftBody := r.FormValue("body")
	if err := s.formProtector.Validate(r.FormValue("form_token")); err != nil {
		message := "Could not verify your post submission."
		switch {
		case errors.Is(err, ErrFreshFormToken):
			message = "Please wait a moment and try again."
		case errors.Is(err, ErrExpiredFormToken), errors.Is(err, ErrInvalidFormToken):
			message = "Form expired. Reload and try again."
		}
		s.renderIndex(w, r, http.StatusBadRequest, indexData{
			FlashError: message,
			DraftBody:  draftBody,
		})
		return
	}

	ip := clientIP(r)
	_, err := s.postService.Create(r.Context(), r.FormValue("body"), ip)
	if err != nil {
		message := "Could not submit your post right now."
		switch {
		case errors.Is(err, posts.ErrEmptyBody):
			message = "Post text cannot be empty."
		case errors.Is(err, posts.ErrTooLong):
			message = fmt.Sprintf("Post is too long. Limit is %d characters.", s.maxChars)
		case errors.Is(err, rate_limit.ErrRateLimited):
			message = "Posting is temporarily rate-limited for this connection."
		default:
			var cooldownErr rate_limit.CooldownError
			if errors.As(err, &cooldownErr) {
				message = fmt.Sprintf("Slow down. Try again in about %s.", cooldownErr.Remaining.Round(time.Second))
			}
		}
		s.renderIndex(w, r, http.StatusBadRequest, indexData{
			FlashError: message,
			DraftBody:  draftBody,
		})
		return
	}

	setFlash(w, flashNoticeCookie, "Post received. It will appear after CW classification finishes.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func readFlash(w http.ResponseWriter, r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}

	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return cookie.Value
}

func setFlash(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}

	if ip := strings.TrimSpace(r.RemoteAddr); ip != "" {
		return ip
	}

	return "unknown"
}

func WaitForShutdown(ctx context.Context, server *http.Server, timeout time.Duration) error {
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func applySecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; style-src 'self'; font-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), microphone=(), payment=(), usb=()")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

func withStaticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		next.ServeHTTP(w, r)
	})
}

func isLikelyMobileUserAgent(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	markers := []string{
		"android",
		"iphone",
		"ipad",
		"ipod",
		"mobile",
		"blackberry",
		"opera mini",
	}
	for _, marker := range markers {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

func buildFeedEntries(items []posts.Post) []feedEntry {
	entries := make([]feedEntry, 0, len(items))
	for i := 0; i < len(items); {
		if !hasLabel(items[i].CWLabels, "spam") {
			entries = append(entries, feedEntry{
				Post: postView{
					Post:   items[i],
					IsSpam: false,
				},
			})
			i++
			continue
		}

		j := i
		spamRun := make([]postView, 0, 2)
		for j < len(items) && hasLabel(items[j].CWLabels, "spam") {
			spamRun = append(spamRun, postView{
				Post:   items[j],
				IsSpam: true,
			})
			j++
		}

		if len(spamRun) == 1 {
			entries = append(entries, feedEntry{Post: spamRun[0]})
		} else {
			entries = append(entries, feedEntry{SpamPosts: spamRun})
		}
		i = j
	}

	return entries
}

func hasLabel(labels []string, wanted string) bool {
	for _, label := range labels {
		if label == wanted {
			return true
		}
	}
	return false
}
