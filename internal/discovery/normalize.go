package discovery

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/purell"
)

// trackingParams are removed from URLs so the same article from different feeds dedupes.
var trackingPrefixes = []string{"utm_", "mc_", "pk_", "hsa_", "ga_"}
var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true, "igshid": true, "ocid": true, "cmpid": true,
	"ref": true, "ref_src": true, "ref_url": true, "src": true, "source": true, "campaign": true, "cid": true,
	"smid": true, "taid": true, "outputtype": true, "_ga": true, "s_cid": true, "partner": true, "taboola": true,
}

// Normalize returns the canonical form of an article URL used as the dedupe key,
// plus its lowercase host. It drops the fragment, tracking parameters, default
// ports and duplicate slashes, sorts the query, and treats http and https as the same.
func Normalize(raw string) (normalized, host string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", &url.Error{Op: "normalize", URL: raw, Err: errUnsupportedScheme}
	}
	if u.Host == "" {
		return "", "", &url.Error{Op: "normalize", URL: raw, Err: errNoHost}
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		drop := trackingParams[lk]
		for _, p := range trackingPrefixes {
			if strings.HasPrefix(lk, p) {
				drop = true
			}
		}
		if drop {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Scheme = "https"
	out := purell.NormalizeURL(u, purell.FlagsSafe|purell.FlagRemoveDotSegments|purell.FlagRemoveDuplicateSlashes|
		purell.FlagRemoveFragment|purell.FlagRemoveTrailingSlash|purell.FlagSortQuery|purell.FlagRemoveEmptyQuerySeparator)
	nu, err := url.Parse(out)
	if err != nil {
		return "", "", err
	}
	return out, strings.ToLower(nu.Hostname()), nil
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const (
	errUnsupportedScheme simpleErr = "unsupported scheme"
	errNoHost            simpleErr = "missing host"
)

// Dedupe collapses candidates that normalize to the same URL, keeping the first
// as primary and recording every (provider, source, query) that found it so
// discovery provenance is preserved. Candidates with invalid URLs are returned
// separately, not silently dropped.
type Deduped struct {
	Normalized string
	Host       string
	Primary    Candidate
	Found      []Candidate // every sighting, including Primary
}

func Dedupe(cands []Candidate) (unique []Deduped, rejected []Candidate) {
	idx := map[string]int{}
	for _, c := range cands {
		n, host, err := Normalize(c.URL)
		if err != nil {
			rejected = append(rejected, c)
			continue
		}
		if i, ok := idx[n]; ok {
			unique[i].Found = append(unique[i].Found, c)
			continue
		}
		idx[n] = len(unique)
		unique = append(unique, Deduped{Normalized: n, Host: host, Primary: c, Found: []Candidate{c}})
	}
	return unique, rejected
}
