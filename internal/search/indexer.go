package search

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// watermarkName identifies the incident index's row in search_index_state.
const watermarkName = "incidents"

// epoch is where a fresh index starts from: before any incident.
var epoch = time.Unix(0, 0).UTC()

// Indexer copies changed incidents from PostgreSQL into OpenSearch. Every change to an incident, including
// merges and withdrawals, bumps its updated_at, so a watermark over (updated_at, id) finds them all
// without any hook in the code that makes the changes. A merged or withdrawn incident is deleted from the
// index instead of written.
type Indexer struct {
	Store *storage.Store
	Index *Client
	// Batch is incidents per bulk request (default 200).
	Batch int
	// MaxBatches bounds one Run (default 20), so a long catch-up is spread over several runs.
	MaxBatches int
	// Lag holds back very recent rows; see ListIncidentsToIndex (default 10s).
	Lag time.Duration
}

// Result says what one run did.
type Result struct {
	Indexed int  // documents written
	Removed int  // documents deleted (merged or withdrawn incidents)
	More    bool // there is still work; the next run continues
}

// Run indexes up to MaxBatches batches.
func (x *Indexer) Run(ctx context.Context) (Result, error) {
	if x.Batch <= 0 {
		x.Batch = 200
	}
	if x.MaxBatches <= 0 {
		x.MaxBatches = 20
	}
	lag := x.Lag
	if lag == 0 {
		lag = 10 * time.Second
	}
	return x.run(ctx, lag)
}

// RunNow is Run without the safety lag, for tests and an operator's rebuild when nothing else is writing.
func (x *Indexer) RunNow(ctx context.Context) (Result, error) {
	if x.Batch <= 0 {
		x.Batch = 200
	}
	if x.MaxBatches <= 0 {
		x.MaxBatches = 20
	}
	return x.run(ctx, 0)
}

func (x *Indexer) run(ctx context.Context, lag time.Duration) (Result, error) {
	var res Result
	if err := x.Index.EnsureIndex(ctx); err != nil {
		return res, err
	}
	q := x.Store.Queries()
	after, afterID := epoch, uuid.Nil
	if wm, err := q.GetSearchWatermark(ctx, watermarkName); err == nil {
		after, afterID = wm.UpdatedAt, wm.LastID
	} else if !storage.IsNoRows(err) {
		return res, err
	}
	for b := 0; b < x.MaxBatches; b++ {
		rows, err := q.ListIncidentsToIndex(ctx, dbgen.ListIncidentsToIndexParams{
			AfterUpdatedAt: after, AfterID: afterID, LagSeconds: lag.Seconds(), MaxRows: int32(x.Batch),
		})
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			return res, nil
		}
		var docs []Doc
		var gone []string
		for _, r := range rows {
			if r.MergedInto != nil || r.VerificationStatus == "withdrawn" {
				gone = append(gone, r.ID.String())
				continue
			}
			docs = append(docs, docFromRow(r))
		}
		if err := x.Index.Apply(ctx, docs, gone); err != nil {
			return res, fmt.Errorf("index batch: %w", err)
		}
		last := rows[len(rows)-1]
		after, afterID = last.UpdatedAt, last.ID
		// Advance only after the batch is in OpenSearch: a crash in between repeats it, which is harmless.
		if err := q.SetSearchWatermark(ctx, dbgen.SetSearchWatermarkParams{Name: watermarkName, UpdatedAt: after, LastID: afterID}); err != nil {
			return res, err
		}
		res.Indexed += len(docs)
		res.Removed += len(gone)
		if len(rows) < x.Batch {
			return res, nil
		}
	}
	res.More = true
	return res, nil
}

// Reset forgets the watermark, so the next run reindexes every incident. It does not delete documents:
// every incident is written again, and ones that should be gone are deleted as they are met.
func Reset(ctx context.Context, st *storage.Store) error {
	return st.Queries().ResetSearchWatermark(ctx, watermarkName)
}

// Status describes how far the indexer has got.
type Status struct {
	Watermark *time.Time // updated_at of the last incident written; nil if never run
	IndexedAt *time.Time // when the indexer last wrote
	Pending   int64      // incidents changed since the watermark
}

// StatusOf reads the indexer's progress from PostgreSQL.
func StatusOf(ctx context.Context, st *storage.Store) (Status, error) {
	q := st.Queries()
	after, afterID := epoch, uuid.Nil
	var out Status
	wm, err := q.GetSearchWatermark(ctx, watermarkName)
	switch {
	case err == nil:
		after, afterID = wm.UpdatedAt, wm.LastID
		out.Watermark, out.IndexedAt = &wm.UpdatedAt, &wm.IndexedAt
	case !storage.IsNoRows(err):
		return out, err
	}
	out.Pending, err = q.CountIncidentsToIndex(ctx, dbgen.CountIncidentsToIndexParams{AfterUpdatedAt: after, AfterID: afterID})
	return out, err
}

func docFromRow(r dbgen.ListIncidentsToIndexRow) Doc {
	d := Doc{
		ID: r.ID.String(), City: deref(r.City), County: deref(r.County), State: deref(r.State), StreetAddress: deref(r.StreetAddress),
		Circumstances: deref(r.Circumstances), Severity: r.Severity, HitAndRun: r.HitAndRun, Verification: r.VerificationStatus,
		VRU: nonNil(r.VruCategories), Vehicles: nonNil(r.VehicleCategories), ArticleCount: int(r.ArticleCount),
		Titles: nonNil(r.Titles), Publishers: nonNil(r.Publishers), Descriptions: nonNil(r.Descriptions), UpdatedAt: r.UpdatedAt,
		Labels: Labels(r.Severity, r.HitAndRun, r.VruCategories, r.VehicleCategories),
	}
	if r.OccurredOn != nil {
		s := r.OccurredOn.Format("2006-01-02")
		d.OccurredOn = &s
	}
	if r.InjuredCount != nil {
		n := int(*r.InjuredCount)
		d.Injured = &n
	}
	if r.FatalityCount != nil {
		n := int(*r.FatalityCount)
		d.Fatalities = &n
	}
	if r.Lat != nil && r.Lon != nil {
		d.Location = &GeoPoint{Lat: *r.Lat, Lon: *r.Lon}
	}
	return d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
