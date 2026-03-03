package posts

import (
	"context"
	"testing"
	"time"

	"ventboard/internal/rate_limit"
)

func TestNormalizeBody(t *testing.T) {
	t.Parallel()

	got := normalizeBody(" \r\nhello\r\nworld\r\n ")
	if got != "hello\nworld" {
		t.Fatalf("unexpected normalized body: %q", got)
	}
}

func TestCreateRejectsEmptyBody(t *testing.T) {
	t.Parallel()

	service := &Service{
		repo:         nil,
		limiter:      rate_limit.NewLimiter(time.Minute, time.Minute, 5, "salt", time.Hour),
		postMaxChars: 10,
		now:          func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}

	if _, err := service.Create(context.Background(), " \n ", "127.0.0.1"); err != ErrEmptyBody {
		t.Fatalf("expected ErrEmptyBody, got %v", err)
	}
}

func TestCreateRejectsTooLongBody(t *testing.T) {
	t.Parallel()

	service := &Service{
		repo:         nil,
		limiter:      rate_limit.NewLimiter(time.Minute, time.Minute, 5, "salt", time.Hour),
		postMaxChars: 3,
		now:          func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}

	if _, err := service.Create(context.Background(), "hello", "127.0.0.1"); err != ErrTooLong {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
}
