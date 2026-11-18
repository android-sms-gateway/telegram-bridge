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

// Service is the public face of the store: it answers what a reply resolves
// to, remembers what the forward path reported, and runs the periodic cleanup
// of expired mappings.
//
// It owns no state of its own: the mappings themselves live in the Repository,
// which stays private to this module — every consumer goes through the service.
type Service struct {
	repo *Repository

	logger *zap.Logger
}

// New creates a new Service from the module's repository.
func New(repo *Repository, logger *zap.Logger) *Service {
	return &Service{
		repo: repo,

		logger: logger,
	}
}

// Put stores the mapping that resolves a reply to the referenced message back
// to the SMS sender. See Repository.Put for the stamping it applies.
func (s *Service) Put(ctx context.Context, mapping Mapping) error {
	if err := s.repo.Put(ctx, mapping); err != nil {
		return err
	}

	s.logger.Debug("mapping stored",
		zap.String("key", mapping.key()),
		zap.String("phone", mapping.Phone),
	)

	return nil
}

// Resolve returns the mapping of the message ref refers to, or
// ErrMappingNotFound when no unexpired record exists for the ref.
func (s *Service) Resolve(ctx context.Context, ref MessageRef) (Mapping, error) {
	return s.repo.Resolve(ctx, ref)
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
// and the next tick retries. Unreadable records are logged too: cleanup keeps
// them on disk for an operator, and the log is how the operator learns of them.
func (s *Service) cleanup(ctx context.Context) {
	result, err := s.repo.CleanupExpired(ctx)
	if err != nil {
		s.logger.Error("store cleanup failed", zap.Error(err))

		return
	}

	for _, skipped := range result.Skipped {
		s.logger.Warn("skipping unreadable mapping",
			zap.String("key", skipped.Key),
			zap.Error(skipped.Err),
		)
	}

	if result.Removed > 0 {
		s.logger.Info("expired mappings removed", zap.Int("removed", result.Removed))
	}
}
