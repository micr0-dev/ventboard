package boardhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const honeypotFieldName = "contact_url"

var (
	ErrInvalidFormToken = errors.New("invalid form token")
	ErrExpiredFormToken = errors.New("expired form token")
	ErrFreshFormToken   = errors.New("form token too fresh")
)

type FormProtector struct {
	secret []byte
	minAge time.Duration
	maxAge time.Duration
	now    func() time.Time
}

func NewFormProtector(secret string, minAge, maxAge time.Duration) *FormProtector {
	return &FormProtector{
		secret: []byte(secret),
		minAge: minAge,
		maxAge: maxAge,
		now:    time.Now,
	}
}

func (p *FormProtector) IssueToken() string {
	issuedAt := p.now().UTC().Unix()
	raw := strconv.FormatInt(issuedAt, 10)
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(raw))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return raw + "." + signature
}

func (p *FormProtector) Validate(token string) error {
	issuedAt, err := p.parse(token)
	if err != nil {
		return err
	}

	age := p.now().UTC().Sub(issuedAt)
	if age < p.minAge {
		return ErrFreshFormToken
	}
	if age > p.maxAge {
		return ErrExpiredFormToken
	}
	return nil
}

func (p *FormProtector) parse(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return time.Time{}, ErrInvalidFormToken
	}

	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	actual, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actual, expected) {
		return time.Time{}, ErrInvalidFormToken
	}

	issuedAtUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, ErrInvalidFormToken
	}

	issuedAt := time.Unix(issuedAtUnix, 0).UTC()
	if issuedAt.After(p.now().UTC().Add(2 * time.Minute)) {
		return time.Time{}, fmt.Errorf("%w: future token", ErrInvalidFormToken)
	}
	return issuedAt, nil
}
