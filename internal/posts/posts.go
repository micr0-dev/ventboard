package posts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ventboard/internal/rate_limit"
)

const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusError     = "error"
)

var (
	ErrEmptyBody = errors.New("post body cannot be empty")
	ErrTooLong   = errors.New("post body exceeds maximum length")
)

type Post struct {
	ID                  int64
	Body                string
	Status              string
	CreatedAt           time.Time
	PublishedAt         *time.Time
	CWLabels            []string
	ClassifierVersion   string
	ClassifierAttempts  int
	LastClassifierError string
	IPHash              string
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) InsertPending(ctx context.Context, body, ipHash, classifierVersion string, now time.Time) (Post, error) {
	result, err := r.db.ExecContext(
		ctx,
		`INSERT INTO posts (
			body, status, created_at, cw_labels_json, classifier_version, classifier_attempts, ip_hash
		) VALUES (?, ?, ?, '[]', ?, 0, ?)`,
		body,
		StatusPending,
		now.UTC().Unix(),
		classifierVersion,
		ipHash,
	)
	if err != nil {
		return Post{}, fmt.Errorf("insert pending post: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Post{}, fmt.Errorf("read insert id: %w", err)
	}

	return Post{
		ID:                id,
		Body:              body,
		Status:            StatusPending,
		CreatedAt:         now.UTC(),
		CWLabels:          []string{},
		ClassifierVersion: classifierVersion,
		IPHash:            ipHash,
	}, nil
}

func (r *Repository) ListPublished(ctx context.Context, limit int) ([]Post, error) {
	rows, err := r.db.QueryContext(
		ctx,
		`SELECT id, body, status, created_at, published_at, cw_labels_json, classifier_version,
		        classifier_attempts, COALESCE(last_classifier_error, ''), ip_hash
		   FROM posts
		  WHERE status = ?
		  ORDER BY created_at DESC
		  LIMIT ?`,
		StatusPublished,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list published posts: %w", err)
	}
	defer rows.Close()

	var result []Post
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, post)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published posts: %w", err)
	}

	return result, nil
}

func (r *Repository) NextPending(ctx context.Context) (Post, error) {
	row := r.db.QueryRowContext(
		ctx,
		`SELECT id, body, status, created_at, published_at, cw_labels_json, classifier_version,
		        classifier_attempts, COALESCE(last_classifier_error, ''), ip_hash
		   FROM posts
		  WHERE status = ?
		  ORDER BY created_at ASC
		  LIMIT 1`,
		StatusPending,
	)

	post, err := scanPost(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Post{}, sql.ErrNoRows
		}
		return Post{}, err
	}
	return post, nil
}

func (r *Repository) MarkPublished(ctx context.Context, id int64, labels []string, classifierVersion string, now time.Time) error {
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("marshal labels: %w", err)
	}

	_, err = r.db.ExecContext(
		ctx,
		`UPDATE posts
		    SET status = ?,
		        published_at = ?,
		        cw_labels_json = ?,
		        classifier_version = ?,
		        last_classifier_error = NULL
		  WHERE id = ?`,
		StatusPublished,
		now.UTC().Unix(),
		string(labelsJSON),
		classifierVersion,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}

func (r *Repository) MarkClassificationFailure(ctx context.Context, id int64, message string, maxRetries int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failure tx: %w", err)
	}
	defer tx.Rollback()

	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT classifier_attempts FROM posts WHERE id = ?`, id).Scan(&attempts); err != nil {
		return fmt.Errorf("read attempts: %w", err)
	}
	attempts++

	status := StatusPending
	if attempts >= maxRetries {
		status = StatusError
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE posts
		    SET classifier_attempts = ?,
		        last_classifier_error = ?,
		        status = ?
		  WHERE id = ?`,
		attempts,
		truncate(message, 500),
		status,
		id,
	); err != nil {
		return fmt.Errorf("update failure info: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failure tx: %w", err)
	}
	return nil
}

func (r *Repository) CountRecentByIPHashes(ctx context.Context, ipHashes []string, since time.Time) (int, error) {
	if len(ipHashes) == 0 {
		return 0, nil
	}

	placeholders := makePlaceholders(len(ipHashes))
	args := make([]any, 0, len(ipHashes)+1)
	for _, hash := range ipHashes {
		args = append(args, hash)
	}
	args = append(args, since.UTC().Unix())

	var count int
	if err := r.db.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT COUNT(*)
		   FROM posts
		  WHERE ip_hash IN (%s)
		    AND created_at >= ?`, placeholders),
		args...,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count recent posts: %w", err)
	}
	return count, nil
}

func (r *Repository) LatestPostTimeByIPHashes(ctx context.Context, ipHashes []string) (time.Time, bool, error) {
	if len(ipHashes) == 0 {
		return time.Time{}, false, nil
	}

	placeholders := makePlaceholders(len(ipHashes))
	args := make([]any, 0, len(ipHashes))
	for _, hash := range ipHashes {
		args = append(args, hash)
	}

	var unix int64
	err := r.db.QueryRowContext(
		ctx,
		fmt.Sprintf(`SELECT created_at
		   FROM posts
		  WHERE ip_hash IN (%s)
		  ORDER BY created_at DESC
		  LIMIT 1`, placeholders),
		args...,
	).Scan(&unix)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("latest post time: %w", err)
	}
	return time.Unix(unix, 0).UTC(), true, nil
}

type Service struct {
	repo              *Repository
	limiter           *rate_limit.Limiter
	postMaxChars      int
	classifierVersion string
	now               func() time.Time
}

func NewService(repo *Repository, limiter *rate_limit.Limiter, postMaxChars int, classifierVersion string) *Service {
	return &Service{
		repo:              repo,
		limiter:           limiter,
		postMaxChars:      postMaxChars,
		classifierVersion: classifierVersion,
		now:               time.Now,
	}
}

func (s *Service) Create(ctx context.Context, body, ip string) (Post, error) {
	normalized := normalizeBody(body)
	if normalized == "" {
		return Post{}, ErrEmptyBody
	}
	if len([]rune(normalized)) > s.postMaxChars {
		return Post{}, ErrTooLong
	}

	now := s.now().UTC()
	if err := s.limiter.Check(ctx, s.repo, ip, now); err != nil {
		return Post{}, err
	}

	return s.repo.InsertPending(ctx, normalized, s.limiter.CurrentIPHash(ip, now), s.classifierVersion, now)
}

func (s *Service) ListPublished(ctx context.Context, limit int) ([]Post, error) {
	return s.repo.ListPublished(ctx, limit)
}

func normalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.TrimSpace(body)
	return body
}

func scanPost(scanner interface{ Scan(dest ...any) error }) (Post, error) {
	var (
		post            Post
		createdAtUnix   int64
		publishedAtUnix sql.NullInt64
		labelsJSON      string
		lastError       string
	)

	if err := scanner.Scan(
		&post.ID,
		&post.Body,
		&post.Status,
		&createdAtUnix,
		&publishedAtUnix,
		&labelsJSON,
		&post.ClassifierVersion,
		&post.ClassifierAttempts,
		&lastError,
		&post.IPHash,
	); err != nil {
		return Post{}, err
	}

	post.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	post.LastClassifierError = lastError
	if publishedAtUnix.Valid {
		publishedAt := time.Unix(publishedAtUnix.Int64, 0).UTC()
		post.PublishedAt = &publishedAt
	}

	if err := json.Unmarshal([]byte(labelsJSON), &post.CWLabels); err != nil {
		return Post{}, fmt.Errorf("decode labels: %w", err)
	}

	return post, nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func makePlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
