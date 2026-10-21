package pipeline

import (
	"context"

	"github.com/google/uuid"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/dedupe"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// FingerprintBatchSize and FingerprintPerRun bound the daily catch-up of articles stored before
// duplicate detection existed.
const (
	FingerprintBatchSize = 200
	FingerprintPerRun    = 5000
)

// FingerprintArticles walks the articles that have text but no canonical key, in id order, and returns
// how many it updated, stopping after max. Articles whose URL is unusable keep a NULL key and are
// skipped by the cursor. It only fills in fingerprints; it does not mark old articles as duplicates.
func FingerprintArticles(ctx context.Context, q *dbgen.Queries, batch int32, max int) (int, error) {
	done, after := 0, uuid.Nil
	for done < max {
		rows, err := q.ListArticlesWithoutFingerprint(ctx, dbgen.ListArticlesWithoutFingerprintParams{ID: after, Limit: batch})
		if err != nil {
			return done, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			after = r.ID
			p := dbgen.BackfillArticleFingerprintParams{ID: r.ID}
			if key := dedupe.CanonicalKey(deref(r.CanonicalUrl), r.FinalUrl); key != "" {
				p.CanonicalKey = &key
			}
			if r.BodyText != nil {
				if fp, ok := dedupe.Fingerprint(*r.BodyText); ok {
					s := int64(fp)
					p.Simhash = &s
				}
			}
			if p.CanonicalKey == nil && p.Simhash == nil {
				continue
			}
			if err := q.BackfillArticleFingerprint(ctx, p); err != nil {
				return done, err
			}
			done++
		}
	}
	return done, nil
}
