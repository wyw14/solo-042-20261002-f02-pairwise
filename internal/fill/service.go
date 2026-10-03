package fill

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-98/internal/model"
	"github.com/wyw14/cry-98/internal/vessel"
)

type Service struct {
	coordinator   *Coordinator
	completion    *CompletionService
	abort         *AbortService
	defaultLiters float64
}

func NewService(coordinator *Coordinator, completion *CompletionService, abort *AbortService, defaultLiters float64) (*Service, error) {
	if defaultLiters <= 0 {
		return nil, errors.New("default fill volume must be positive")
	}
	return &Service{coordinator: coordinator, completion: completion, abort: abort, defaultLiters: defaultLiters}, nil
}

func (s *Service) OnLowLevel(ctx context.Context, vesselID string, observedAt time.Time) (model.FillSession, error) {
	_ = observedAt
	// The automatic default is still validated against level and capacity by
	// the coordinator before the valve opens.
	return s.coordinator.StartAutomatic(ctx, vesselID, s.defaultLiters)
}

// StartManual rejects non-positive or non-finite volumes explicitly instead
// of silently substituting the automatic default; the operator must see why
// a request was refused.
func (s *Service) StartManual(ctx context.Context, vesselID string, liters float64) (model.FillSession, error) {
	if math.IsNaN(liters) || math.IsInf(liters, 0) || liters <= 0 {
		return model.FillSession{}, vessel.ErrInvalidFillVolume
	}
	return s.coordinator.StartManual(ctx, vesselID, liters)
}

func (s *Service) BeginSoak(id uuid.UUID) (model.FillSession, error) {
	return s.coordinator.BeginSoak(id)
}

func (s *Service) Complete(ctx context.Context, id uuid.UUID) (model.FillSession, error) {
	return s.completion.Complete(ctx, id)
}

func (s *Service) Abort(ctx context.Context, id uuid.UUID, reason string) (model.FillSession, error) {
	return s.abort.Abort(ctx, id, reason)
}

func (s *Service) RecoverIsolation(id uuid.UUID) error {
	return s.abort.RecoverIsolation(id)
}

func (s *Service) Get(id uuid.UUID) (model.FillSession, bool) {
	return s.coordinator.Get(id)
}

func (s *Service) List() []model.FillSession {
	return s.coordinator.List()
}
