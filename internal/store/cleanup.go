package store

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// cleanupInterval is how often the background cleanup drops expired mappings.
// The bucket holds one record per forwarded message, so a quarter of an hour is
// both far below the memory a stale record can hold and far above the work the
// scan costs.
const cleanupInterval = 15 * time.Minute

// Service runs the periodic cleanup of expired mappings. It owns no state of
// its own: the mappings themselves live in the Repository.
type Service struct {
	repo *Repository

	logger *zap.Logger
}

// New creates a new cleanup Service.
func New(repo *Repository, logger *zap.Logger) *Service {
	return &Service{
		repo: repo,

		logger: logger,
	}
}

// Run drops the expired mappings on start and then once per cleanup interval,
// until ctx is canceled.
//
// It is registered as an fx runnable, so it returns only at shutdown, and always
// with a nil error.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("store cleanup started", zap.Duration("interval", cleanupInterval))

	// The first pass runs before the first tick so a restart does not wait a
	// full interval to drop what expired while the process was down.
	s.cleanup(ctx)

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("store cleanup stopped")

			return nil
		case <-ticker.C:
			s.cleanup(ctx)
		}
	}
}

// cleanup drops the expired mappings once. A failure is logged and the loop
// carries on: a transient storage error must not take the whole service down,
// and the next tick retries.
func (s *Service) cleanup(ctx context.Context) {
	removed, err := s.repo.CleanupExpired(ctx)
	if err != nil {
		s.logger.Error("store cleanup failed", zap.Error(err))

		return
	}

	if removed > 0 {
		s.logger.Info("expired mappings removed", zap.Int("removed", removed))
	}
}
