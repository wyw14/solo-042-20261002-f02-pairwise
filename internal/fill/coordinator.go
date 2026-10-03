package fill

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-98/internal/circuit"
	"github.com/wyw14/cry-98/internal/model"
	"github.com/wyw14/cry-98/internal/valve"
	"github.com/wyw14/cry-98/internal/vessel"
)

var ErrActiveSession = errors.New("vessel already has an active fill session")

type Coordinator struct {
	mu          sync.RWMutex
	sessions    map[uuid.UUID]model.FillSession
	active      map[string]uuid.UUID
	held        map[string]float64
	circuits    *circuit.Manager
	circuitList *circuit.Registry
	routes      *valve.RouteService
	retry       *RetryService
	vessels     *vessel.StateService
	capacities  *vessel.CapacityRegistry
	now         func() time.Time
}

func (c *Coordinator) SetRetryService(retry *RetryService) {
	c.mu.Lock()
	c.retry = retry
	c.mu.Unlock()
}

func NewCoordinator(circuits *circuit.Manager, circuitList *circuit.Registry, routes *valve.RouteService, vessels *vessel.StateService, capacities *vessel.CapacityRegistry, now func() time.Time) *Coordinator {
	if now == nil {
		now = time.Now
	}
	return &Coordinator{
		sessions:    make(map[uuid.UUID]model.FillSession),
		active:      make(map[string]uuid.UUID),
		held:        make(map[string]float64),
		circuits:    circuits,
		circuitList: circuitList,
		routes:      routes,
		vessels:     vessels,
		capacities:  capacities,
		now:         now,
	}
}

func (c *Coordinator) Start(ctx context.Context, vesselID string, origin model.FillOrigin, liters float64) (model.FillSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.active[vesselID]; exists {
		return model.FillSession{}, ErrActiveSession
	}
	if err := c.checkPlan(vesselID, liters); err != nil {
		return model.FillSession{}, err
	}
	selected, err := c.selectCircuit()
	if err != nil {
		return model.FillSession{}, err
	}
	session, err := model.NewFillSession(vesselID, origin, 1, liters, c.now())
	if err != nil {
		return model.FillSession{}, err
	}
	reservation, err := c.circuits.Reserve(selected.ID, vesselID, session.ID, c.now())
	if err != nil {
		return model.FillSession{}, err
	}
	session = session.WithCircuit(selected.ID, c.now())
	session, err = session.Transition(model.FillPrechecking, c.now())
	if err != nil {
		_ = c.circuits.Release(selected.ID, session.ID)
		return model.FillSession{}, err
	}
	if _, err := c.vessels.BeginFill(vesselID); err != nil {
		_ = c.circuits.Release(selected.ID, session.ID)
		return model.FillSession{}, err
	}
	route := valve.Route{
		SessionID:         session.ID,
		CircuitID:         selected.ID,
		CircuitGeneration: reservation.Generation,
		ValveID:           selected.ValveID,
		OpenCommandID:     uuid.New(),
		CloseCommandID:    uuid.New(),
	}
	var openErr error
	if c.retry != nil {
		_, openErr = c.retry.Open(ctx, session.ID, reservation.Generation, selected.ValveID)
		if openErr == nil {
			openErr = c.routes.TrackOpen(route)
		}
	} else {
		openErr = c.routes.Open(ctx, route)
	}
	if openErr != nil {
		_, _ = c.vessels.FinishFill(vesselID)
		_ = c.circuits.Release(selected.ID, session.ID)
		return model.FillSession{}, openErr
	}
	session, err = session.Transition(model.FillFilling, c.now())
	if err != nil {
		return model.FillSession{}, err
	}
	c.sessions[session.ID] = session
	c.active[vesselID] = session.ID
	c.held[vesselID] += session.PlannedLiters
	return session, nil
}

func (c *Coordinator) StartAutomatic(ctx context.Context, vesselID string, liters float64) (model.FillSession, error) {
	return c.Start(ctx, vesselID, model.FillAutomatic, liters)
}

func (c *Coordinator) StartManual(ctx context.Context, vesselID string, liters float64) (model.FillSession, error) {
	return c.Start(ctx, vesselID, model.FillManual, liters)
}

func (c *Coordinator) BeginSoak(sessionID uuid.UUID) (model.FillSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[sessionID]
	if !ok {
		return model.FillSession{}, errors.New("fill session not found")
	}
	next, err := session.Transition(model.FillSoaking, c.now())
	if err != nil {
		return model.FillSession{}, err
	}
	c.sessions[sessionID] = next
	return next, nil
}

func (c *Coordinator) Get(id uuid.UUID) (model.FillSession, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	session, ok := c.sessions[id]
	return session, ok
}

func (c *Coordinator) List() []model.FillSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]model.FillSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		result = append(result, session)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

// checkPlan validates a planned fill against the vessel's capacity profile
// and current level. It runs under c.mu together with the hold recorded at
// the end of Start, so concurrent starts never approve the same available
// capacity twice.
func (c *Coordinator) checkPlan(vesselID string, liters float64) error {
	current, ok := c.vessels.Registry().Get(vesselID)
	if !ok {
		return &PlanRejection{
			Reason:   ReasonVesselUnknown,
			VesselID: vesselID,
			Planned:  liters,
			Detail:   fmt.Sprintf("vessel %s is not registered", vesselID),
		}
	}
	profile, ok := c.capacities.Get(vesselID)
	if !ok {
		return &PlanRejection{
			Reason:   ReasonCapacityProfileMissing,
			VesselID: vesselID,
			Planned:  liters,
			Detail:   fmt.Sprintf("vessel %s has no capacity profile", vesselID),
		}
	}
	if rejection := validatePlan(vesselID, liters, current, profile, c.held[vesselID]); rejection != nil {
		return rejection
	}
	return nil
}

// releaseHold returns a session's planned liters to the vessel's available
// capacity. Callers must hold c.mu.
func (c *Coordinator) releaseHold(session model.FillSession) {
	remaining := c.held[session.VesselID] - session.PlannedLiters
	if remaining <= 0 {
		delete(c.held, session.VesselID)
		return
	}
	c.held[session.VesselID] = remaining
}

func (c *Coordinator) selectCircuit() (circuit.Circuit, error) {
	for _, candidate := range c.circuitList.List() {
		if !candidate.Enabled {
			continue
		}
		if _, occupied := c.circuits.Get(candidate.ID); !occupied {
			return candidate, nil
		}
	}
	return circuit.Circuit{}, circuit.ErrCircuitBusy
}

func (c *Coordinator) finishState(session model.FillSession) {
	c.sessions[session.ID] = session
	delete(c.active, session.VesselID)
	c.releaseHold(session)
}
