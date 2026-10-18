package incidents

import (
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage/dbgen"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func f32p(f float64) *float32 { v := float32(f); return &v }

func i32p(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}
func intp(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func insertParams(st State) dbgen.InsertIncidentParams {
	return dbgen.InsertIncidentParams{
		OccurredOn: st.OccurredOn, OccurredAt: st.OccurredAt, OccurredPrecision: st.OccurredPrecision, Timezone: strp(st.Timezone),
		City: strp(st.City), County: strp(st.County), State: strp(st.State), Country: "US", StreetAddress: strp(st.StreetAddress),
		LocationPrecision: st.LocationPrecision, GeocodeSource: strp(st.GeocodeSource), GeocodeConfidence: st.GeocodeConfidence,
		GeocodePrecision: strp(st.GeocodePrecision), VruCategories: nz(st.VRU), VehicleCategories: nz(st.Vehicles),
		InjuredCount: i32p(st.Injured), FatalityCount: i32p(st.Fatal), Severity: st.Severity, HitAndRun: st.HitAndRun,
		Circumstances: strp(st.Circumstances), FactsStatus: st.FactsStatus, Lon: st.Lon, Lat: st.Lat,
	}
}

func updateParams(id uuid.UUID, st State, verification string, articles int32, lat, lon *float64) dbgen.UpdateIncidentCanonicalParams {
	return dbgen.UpdateIncidentCanonicalParams{
		ID: id, OccurredOn: st.OccurredOn, OccurredAt: st.OccurredAt, OccurredPrecision: st.OccurredPrecision, Timezone: strp(st.Timezone),
		City: strp(st.City), County: strp(st.County), State: strp(st.State), StreetAddress: strp(st.StreetAddress),
		LocationPrecision: st.LocationPrecision, VruCategories: nz(st.VRU), VehicleCategories: nz(st.Vehicles),
		InjuredCount: i32p(st.Injured), FatalityCount: i32p(st.Fatal), Severity: st.Severity, HitAndRun: st.HitAndRun,
		Circumstances: strp(st.Circumstances), FactsStatus: st.FactsStatus, VerificationStatus: verification,
		ArticleCount: articles, Lon: lon, Lat: lat,
	}
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func stateFromView(v dbgen.IncidentsV) State {
	return State{
		OccurredOn: v.OccurredOn, OccurredAt: v.OccurredAt, OccurredPrecision: v.OccurredPrecision, Timezone: deref(v.Timezone),
		City: deref(v.City), County: deref(v.County), State: deref(v.State), StreetAddress: deref(v.StreetAddress),
		LocationPrecision: v.LocationPrecision, Lat: v.Lat, Lon: v.Lon, VRU: v.VruCategories, Vehicles: v.VehicleCategories,
		Injured: intp(v.InjuredCount), Fatal: intp(v.FatalityCount), Severity: v.Severity, HitAndRun: v.HitAndRun,
		Circumstances: deref(v.Circumstances), FactsStatus: v.FactsStatus,
	}
}

func stateFromRow(r dbgen.FindCandidatesRow) State {
	return stateFromView(dbgen.IncidentsV{
		ID: r.ID, OccurredOn: r.OccurredOn, OccurredAt: r.OccurredAt, OccurredPrecision: r.OccurredPrecision, Timezone: r.Timezone,
		City: r.City, County: r.County, State: r.State, StreetAddress: r.StreetAddress, LocationPrecision: r.LocationPrecision,
		Lat: r.Lat, Lon: r.Lon, VruCategories: r.VruCategories, VehicleCategories: r.VehicleCategories,
		InjuredCount: r.InjuredCount, FatalityCount: r.FatalityCount, Severity: r.Severity, HitAndRun: r.HitAndRun,
		Circumstances: r.Circumstances, FactsStatus: r.FactsStatus,
	})
}
