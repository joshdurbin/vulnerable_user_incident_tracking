package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

// filter is the set of optional incident filters shared by listing and statistics.
type filter struct {
	minLon, minLat, maxLon, maxLat *float64
	from, to                       *time.Time
	state, city                    *string
	vru                            []string
	severity, verification, hitRun *string
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

func parseBbox(s *string) (minLon, minLat, maxLon, maxLat *float64, err error) {
	if s == nil || *s == "" {
		return
	}
	parts := strings.Split(*s, ",")
	if len(parts) != 4 {
		return nil, nil, nil, nil, badRequest("bbox must be minLon,minLat,maxLon,maxLat")
	}
	v := make([]float64, 4)
	for i, p := range parts {
		f, perr := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if perr != nil {
			return nil, nil, nil, nil, badRequest(fmt.Sprintf("bbox: %q is not a number", p))
		}
		v[i] = f
	}
	if v[0] < -180 || v[2] > 180 || v[1] < -90 || v[3] > 90 || v[0] >= v[2] || v[1] >= v[3] {
		return nil, nil, nil, nil, badRequest("bbox out of range or min >= max")
	}
	return &v[0], &v[1], &v[2], &v[3], nil
}

func str[T ~string](p *T) *string {
	if p == nil || *p == "" {
		return nil
	}
	s := string(*p)
	return &s
}

func newFilter(bbox *string, from, to *time.Time, state, city *string, vru []string, severity, verification, hitRun *string) (filter, error) {
	f := filter{from: from, to: to, state: state, city: city, vru: vru, severity: severity, verification: verification, hitRun: hitRun}
	var err error
	if f.minLon, f.minLat, f.maxLon, f.maxLat, err = parseBbox(bbox); err != nil {
		return f, err
	}
	if from != nil && to != nil && to.Before(*from) {
		return f, badRequest("'to' is before 'from'")
	}
	if state != nil {
		s := strings.ToUpper(*state)
		f.state = &s
	}
	if len(vru) == 0 {
		f.vru = nil
	}
	return f, nil
}

func (f filter) list(limit, offset int) dbgen.ListIncidentsAPIParams {
	return dbgen.ListIncidentsAPIParams{
		MinLon: f.minLon, MinLat: f.minLat, MaxLon: f.maxLon, MaxLat: f.maxLat, FromDate: f.from, ToDate: f.to,
		State: f.state, City: f.city, Vru: f.vru, Severity: f.severity, Verification: f.verification, HitAndRun: f.hitRun,
		MaxRows: int32(limit), SkipRows: int32(offset),
	}
}

func (f filter) summary() dbgen.StatsSummaryAPIParams {
	return dbgen.StatsSummaryAPIParams{
		MinLon: f.minLon, MinLat: f.minLat, MaxLon: f.maxLon, MaxLat: f.maxLat, FromDate: f.from, ToDate: f.to,
		State: f.state, City: f.city, Vru: f.vru, Severity: f.severity, Verification: f.verification, HitAndRun: f.hitRun,
	}
}

func (f filter) timeseries(bucket string) dbgen.StatsTimeseriesAPIParams {
	return dbgen.StatsTimeseriesAPIParams{
		Bucket: bucket, MinLon: f.minLon, MinLat: f.minLat, MaxLon: f.maxLon, MaxLat: f.maxLat, FromDate: f.from, ToDate: f.to,
		State: f.state, City: f.city, Vru: f.vru, Severity: f.severity, Verification: f.verification, HitAndRun: f.hitRun,
	}
}

func (f filter) breakdown(dim string, limit int) dbgen.StatsBreakdownAPIParams {
	return dbgen.StatsBreakdownAPIParams{
		Dim: dim, MaxRows: int32(limit), MinLon: f.minLon, MinLat: f.minLat, MaxLon: f.maxLon, MaxLat: f.maxLat, FromDate: f.from, ToDate: f.to,
		State: f.state, City: f.city, Vru: f.vru, Severity: f.severity, Verification: f.verification, HitAndRun: f.hitRun,
	}
}
