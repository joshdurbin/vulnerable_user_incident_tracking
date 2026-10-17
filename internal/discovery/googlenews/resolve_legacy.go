package googlenews

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// LegacyDecoder recovers the publisher URL from older-style article IDs, which
// embed it in a base64 protobuf-like payload. Newer IDs (the "AU_yqL..." form,
// which is what the feed returns today) are encrypted and fail here by design,
// falling through to the next resolver.
type LegacyDecoder struct{}

func (LegacyDecoder) Name() string { return "legacy-base64" }

var embeddedURL = regexp.MustCompile(`https?://[\x21-\x7e]+`)

func (LegacyDecoder) Resolve(_ context.Context, googleURL string) (string, error) {
	id, err := articleID(googleURL)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(id, "="))
	if err != nil {
		return "", err
	}
	m := embeddedURL.Find(raw)
	if m == nil {
		return "", errors.New("legacy-base64: no embedded URL (new-style encrypted id)")
	}
	u := string(m)
	if _, err := url.Parse(u); err != nil {
		return "", err
	}
	return u, nil
}

// articleID extracts the id from .../articles/<id>[?...].
func articleID(googleURL string) (string, error) {
	u, err := url.Parse(googleURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "articles" {
		return "", errors.New("not a google news article url")
	}
	return parts[len(parts)-1], nil
}
