package api

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/pgrepo"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/registry"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/discovery/sourcefile"
	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/storage"
)

// scheduleSyncTimeout bounds the schedule sync after seeding, so a Temporal outage cannot hold up startup.
const scheduleSyncTimeout = 20 * time.Second

// SeedSources fills an empty database with the sources in a seed file. It runs on every API start
// and does nothing once any source exists, so edits made in the admin UI are never overwritten and
// removing the seed file from the configuration is always safe. An unreadable or invalid file is an
// error: the file was configured on purpose, and a rollout that cannot seed should not look healthy.
//
// Seeded sources get their Temporal Schedules straight away. If Temporal is unreachable that is
// logged and left for "Sync schedules" (or the next deploy's bootstrap job), because the sources
// themselves are saved.
func SeedSources(ctx context.Context, st *storage.Store, wf Workflows, reg *discovery.Registry, path string, log zerolog.Logger) error {
	if path == "" {
		return nil
	}
	f, err := sourcefile.Load(path)
	if err != nil {
		return fmt.Errorf("seed file: %w", err)
	}
	if reg == nil {
		reg = registry.New(registry.Deps{})
	}
	if errs := sourcefile.Validate(reg, f); len(errs) > 0 {
		for _, e := range errs {
			log.Error().Err(e).Str("file", path).Msg("seed file problem")
		}
		return fmt.Errorf("seed file %s has %d problem(s); first: %w", path, len(errs), errs[0])
	}
	repo := pgrepo.New(st)
	ns, nq, seeded, err := repo.SeedIfEmpty(ctx, f)
	if err != nil {
		return fmt.Errorf("seed sources: %w", err)
	}
	if !seeded {
		log.Info().Msg("sources already exist; seed file not applied")
		return nil
	}
	log.Info().Int("sources", ns).Int("queries", nq).Str("file", path).Msg("database had no sources; seeded from file")

	srcs, err := repo.Sources(ctx, false)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, scheduleSyncTimeout)
	defer cancel()
	if _, _, _, err := wf.SyncSchedules(sctx, srcs); err != nil {
		log.Warn().Err(err).Msg("seeded sources have no schedules yet; use Sync schedules in the admin UI once Temporal is reachable")
	}
	return nil
}
