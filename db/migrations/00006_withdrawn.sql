-- +goose Up
-- Reprocessing can remove every article supporting an incident; such incidents are
-- withdrawn (kept for audit, hidden from matching and listings), never deleted.
ALTER TABLE incidents DROP CONSTRAINT incidents_verification_status_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_verification_status_check
    CHECK (verification_status IN ('unverified','corroborated','reviewed','withdrawn'));
ALTER TABLE incident_events DROP CONSTRAINT incident_events_event_type_check;
ALTER TABLE incident_events ADD CONSTRAINT incident_events_event_type_check
    CHECK (event_type IN ('created','updated','conflict','merged','split','review_approved','review_rejected','geocoded','withdrawn','superseded'));

-- +goose Down
ALTER TABLE incident_events DROP CONSTRAINT incident_events_event_type_check;
ALTER TABLE incident_events ADD CONSTRAINT incident_events_event_type_check
    CHECK (event_type IN ('created','updated','conflict','merged','split','review_approved','review_rejected','geocoded'));
ALTER TABLE incidents DROP CONSTRAINT incidents_verification_status_check;
ALTER TABLE incidents ADD CONSTRAINT incidents_verification_status_check
    CHECK (verification_status IN ('unverified','corroborated','reviewed'));
