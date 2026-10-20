// Package dedupe recognises articles that repeat another article: the same page under another URL,
// byte-identical text, or a wire story republished with small edits.
//
// URL equality alone misses all three. The checks here run once an article is fetched and its text
// is known, so they sit apart from discovery's URL normalization.
package dedupe

import (
	"bytes"
	"regexp"
	"time"

	"github.com/mfonda/simhash"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
)

const (
	// MaxDistance is the largest Hamming distance between two SimHashes still treated as one story.
	// Of 64 bits, 3 tolerates a changed headline, byline or paragraph on a full-length article while
	// keeping two different stories about the same crash well apart (they differ in 10+ bits).
	MaxDistance = 3
	// MinWords is the shortest text that gets a SimHash. Short items (briefs, "developing" posts)
	// share most of their words by chance, so they are only matched on exact text or canonical URL.
	MinWords = 80
	// Window bounds how far back near-duplicates are searched. Syndicated copies appear within days.
	Window = 14 * 24 * time.Hour
	// shingle is the number of consecutive words per feature.
	shingle = 3
)

// Kinds record how a duplicate was recognised. They match the duplicate_kind column.
const (
	KindContentHash   = "content_hash"
	KindCanonicalURL  = "canonical_url"
	KindNearDuplicate = "near_duplicate"
)

var wordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)

// Fingerprint returns the SimHash of text's word shingles. ok is false when the text is too short to
// compare reliably. Case, punctuation and spacing do not affect the result.
func Fingerprint(text string) (fp uint64, ok bool) {
	words := wordRE.FindAll(bytes.ToLower([]byte(text)), -1)
	if len(words) < MinWords {
		return 0, false
	}
	return simhash.SimhashBytes(simhash.Shingle(shingle, words)), true
}

// Distance is the number of differing bits between two fingerprints.
func Distance(a, b uint64) int { return int(simhash.Compare(a, b)) }

// CanonicalKey is the comparison key for "the same page": the page's declared canonical URL, else the
// URL it finally loaded, normalized like a discovered URL (scheme, tracking parameters, trailing slash).
// It is empty when neither is a usable URL.
func CanonicalKey(canonicalURL, finalURL string) string {
	for _, u := range []string{canonicalURL, finalURL} {
		if u == "" {
			continue
		}
		if n, _, err := discovery.Normalize(u); err == nil {
			return n
		}
	}
	return ""
}
