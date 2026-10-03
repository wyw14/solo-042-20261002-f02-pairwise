package vessel

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-98/internal/model"
)

var (
	// ErrInvalidFillVolume is returned for non-positive or non-finite planned
	// litres. Manual requests are rejected with this instead of being silently
	// replaced with a default volume.
	ErrInvalidFillVolume = errors.New("fill volume must be a positive finite number of litres")
	// ErrGeometryNotConfigured means the vessel has no mm-to-litre mapping or
	// fill limits, so no plan can be validated.
	ErrGeometryNotConfigured = errors.New("vessel geometry is not configured")
	// ErrLevelUnknown means no level probe reading has been received yet, so
	// the available capacity cannot be computed.
	ErrLevelUnknown = errors.New("vessel current level is unknown")
	// ErrExceedsSingleFill means the plan exceeds the per-session litre cap.
	ErrExceedsSingleFill = errors.New("fill volume exceeds the vessel single-fill limit")
	// ErrInsufficientCapacity means the plan does not fit below the safe fill
	// level once reserve headspace and existing reservations are accounted for.
	ErrInsufficientCapacity = errors.New("fill volume exceeds the available vessel capacity")
	// ErrDuplicateReservation indicates a session reserved capacity twice.
	ErrDuplicateReservation = errors.New("fill session already reserved vessel capacity")
)

// CapacityService validates planned fill volumes against vessel geometry and
// the current probe level, and reserves the required headspace for the whole
// lifetime of a fill session. All decisions happen under one mutex, so
// concurrent requests can never be approved against the same available
// capacity.
type CapacityService struct {
	mu       sync.Mutex
	states   *StateService
	reserved map[string]map[uuid.UUID]float64
}

func NewCapacityService(states *StateService) *CapacityService {
	return &CapacityService{states: states, reserved: make(map[string]map[uuid.UUID]float64)}
}

// AvailableLiters reports the headspace still open for new plans on a vessel:
// safe capacity minus reserve band, current contents, and in-flight
// reservations.
func (s *CapacityService) AvailableLiters(vesselID string) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.states.Registry().Get(vesselID)
	if !ok {
		return 0, errors.New("vessel not found")
	}
	if v.Geometry == nil {
		return 0, ErrGeometryNotConfigured
	}
	if !v.LevelKnown {
		return 0, ErrLevelUnknown
	}
	return v.Geometry.AvailableLiters(v.LevelMM, s.reservedLitersLocked(vesselID)), nil
}

// PlanFill validates a manual or automatic plan before any circuit is
// reserved or valve opened, and atomically reserves the requested litres.
func (s *CapacityService) PlanFill(vesselID string, sessionID uuid.UUID, liters float64) error {
	if math.IsNaN(liters) || math.IsInf(liters, 0) || liters <= 0 {
		return ErrInvalidFillVolume
	}
	if sessionID == uuid.Nil {
		return errors.New("fill session id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.states.Registry().Get(vesselID)
	if !ok {
		return errors.New("vessel not found")
	}
	if v.Geometry == nil {
		return fmt.Errorf("%w: vessel %s has no geometry configuration", ErrGeometryNotConfigured, vesselID)
	}
	if !v.LevelKnown {
		return fmt.Errorf("%w: no level reading has been received for vessel %s", ErrLevelUnknown, vesselID)
	}

	bySession, exists := s.reserved[vesselID]
	if exists {
		if _, duplicate := bySession[sessionID]; duplicate {
			return fmt.Errorf("%w: session %s", ErrDuplicateReservation, sessionID)
		}
	}

	if liters > v.Geometry.MaxSingleLiters {
		return fmt.Errorf(
			"%w: requested %.3f L, vessel %s allows at most %.3f L per fill",
			ErrExceedsSingleFill, liters, vesselID, v.Geometry.MaxSingleLiters,
		)
	}

	current := v.Geometry.VolumeLiters(v.LevelMM)
	ceiling := v.Geometry.SafeLiters() - v.Geometry.ReserveLiters()
	reserved := s.reservedLitersLocked(vesselID)
	available := v.Geometry.AvailableLiters(v.LevelMM, reserved)
	if liters > available {
		return fmt.Errorf(
			"%w: requested %.3f L but only %.3f L is available (current %.3f L, limit %.3f L, already reserved %.3f L)",
			ErrInsufficientCapacity, liters, available, current, ceiling, reserved,
		)
	}

	if bySession == nil {
		bySession = make(map[uuid.UUID]float64)
		s.reserved[vesselID] = bySession
	}
	bySession[sessionID] = liters
	return nil
}

// CommitFill converts a reservation into a modelled level increase once a
// session completes successfully, so the consumed headspace stays accounted
// for even before fresh level telemetry arrives. Removing the reservation and
// advancing the level happen in one critical section, so concurrent plans can
// never observe the headspace as free.
func (s *CapacityService) CommitFill(vesselID string, sessionID uuid.UUID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	liters, ok := s.takeLocked(vesselID, sessionID)
	if !ok {
		return nil
	}
	_, err := s.states.Registry().Update(vesselID, func(current model.Vessel) (model.Vessel, error) {
		return current.WithAddedVolume(liters, now)
	})
	return err
}

// ReleaseFill returns a reservation to the pool when a session is aborted,
// fails before opening, or is recovered from isolation. It is idempotent.
func (s *CapacityService) ReleaseFill(vesselID string, sessionID uuid.UUID) {
	s.mu.Lock()
	s.takeLocked(vesselID, sessionID)
	s.mu.Unlock()
}

func (s *CapacityService) reservedLitersLocked(vesselID string) float64 {
	var total float64
	for _, liters := range s.reserved[vesselID] {
		total += liters
	}
	return total
}

func (s *CapacityService) takeLocked(vesselID string, sessionID uuid.UUID) (float64, bool) {
	bySession, ok := s.reserved[vesselID]
	if !ok {
		return 0, false
	}
	liters, ok := bySession[sessionID]
	if !ok {
		return 0, false
	}
	delete(bySession, sessionID)
	if len(bySession) == 0 {
		delete(s.reserved, vesselID)
	}
	return liters, true
}
