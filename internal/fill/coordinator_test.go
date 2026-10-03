package fill

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/circuit"
	"github.com/wyw14/cry-98/internal/model"
	"github.com/wyw14/cry-98/internal/valve"
	"github.com/wyw14/cry-98/internal/vessel"
)

// newTestCoordinator builds a coordinator over a single circuit and a single
// vessel at the given level. A nil profile leaves the capacity registry empty.
func newTestCoordinator(t *testing.T, vesselID string, levelMM float64, profile *model.VesselCapacity) (*Coordinator, *vessel.Registry) {
	t.Helper()
	now := time.Now
	vesselRegistry := vessel.NewRegistry()
	item, err := model.NewVessel(vesselID, "test vessel", "test", now())
	if err != nil {
		t.Fatal(err)
	}
	item = item.WithTelemetry(levelMM, 100, -180, now())
	if err := vesselRegistry.Register(item); err != nil {
		t.Fatal(err)
	}
	capacities := vessel.NewCapacityRegistry()
	if profile != nil {
		if err := capacities.Register(*profile); err != nil {
			t.Fatal(err)
		}
	}
	controller, err := valve.NewController(valve.NewMemoryTransport(now), now)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(
		circuit.NewManager("C-A"),
		circuit.NewRegistry(circuit.Circuit{ID: "C-A", Zone: "test", ValveID: "VALVE-A", Enabled: true}),
		valve.NewRouteService(valve.NewCommandService(controller, now)),
		vessel.NewStateService(vesselRegistry, now),
		capacities,
		now,
	)
	return coordinator, vesselRegistry
}

func coordinatorTestProfile(t *testing.T) model.VesselCapacity {
	t.Helper()
	profile, err := model.NewVesselCapacity("V-01", 0.5, 100, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func (c *Coordinator) heldLiters(vesselID string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.held[vesselID]
}

func TestStartRejectsPlanBeforeOpeningValve(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, vessels := newTestCoordinator(t, "V-01", 100, &profile)
	// available = 100 - 10 - 50 = 40 L; 45 L exceeds the single fill limit.
	if _, err := coordinator.StartManual(context.Background(), "V-01", 45); err == nil {
		t.Fatal("plan above the single fill limit was accepted")
	} else {
		var rejection *PlanRejection
		if !errors.As(err, &rejection) {
			t.Fatalf("error type %T is not a plan rejection", err)
		}
		if rejection.Reason != ReasonExceedsSingleFillLimit {
			t.Fatalf("reason=%s", rejection.Reason)
		}
	}
	if _, occupied := coordinator.circuits.Get("C-A"); occupied {
		t.Fatal("rejected plan reserved the circuit")
	}
	current, ok := vessels.Get("V-01")
	if !ok {
		t.Fatal("vessel missing")
	}
	if current.Status != model.VesselReady {
		t.Fatalf("vessel status=%s after rejection", current.Status)
	}
	if got := len(coordinator.List()); got != 0 {
		t.Fatalf("sessions=%d after rejection", got)
	}
}

func TestStartRejectsUnknownVesselAndMissingProfile(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, _ := newTestCoordinator(t, "V-01", 100, &profile)
	_, err := coordinator.StartManual(context.Background(), "V-99", 10)
	var rejection *PlanRejection
	if !errors.As(err, &rejection) || rejection.Reason != ReasonVesselUnknown {
		t.Fatalf("err=%v", err)
	}

	bare, _ := newTestCoordinator(t, "V-01", 100, nil)
	_, err = bare.StartManual(context.Background(), "V-01", 10)
	if !errors.As(err, &rejection) || rejection.Reason != ReasonCapacityProfileMissing {
		t.Fatalf("err=%v", err)
	}
}

func TestStartValidatesAutomaticPlans(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, _ := newTestCoordinator(t, "V-01", 100, &profile)
	// available = 40 L; an automatic plan of 50 L must be refused too.
	_, err := coordinator.StartAutomatic(context.Background(), "V-01", 50)
	var rejection *PlanRejection
	if !errors.As(err, &rejection) || rejection.Reason != ReasonExceedsSingleFillLimit {
		t.Fatalf("err=%v", err)
	}
}

func TestManualStartRejectsNonPositiveInsteadOfDefaulting(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, _ := newTestCoordinator(t, "V-01", 100, &profile)
	service := &Service{coordinator: coordinator, defaultLiters: 2.1}
	for _, liters := range []float64{0, -2.1} {
		_, err := service.StartManual(context.Background(), "V-01", liters)
		var rejection *PlanRejection
		if !errors.As(err, &rejection) {
			t.Fatalf("liters=%v: error type %T is not a plan rejection", liters, err)
		}
		if rejection.Reason != ReasonInvalidPlannedLiters {
			t.Fatalf("liters=%v: reason=%s", liters, rejection.Reason)
		}
	}
	if got := len(coordinator.List()); got != 0 {
		t.Fatalf("sessions=%d; non-positive requests must not fall back to the default volume", got)
	}
}

func TestConcurrentStartsDoNotReuseAvailableCapacity(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, _ := newTestCoordinator(t, "V-01", 100, &profile)
	// available = 40 L; every worker asks for 30 L, only one may be approved.
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	approved := 0.0
	busy := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := coordinator.StartManual(context.Background(), "V-01", 30)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
				approved += session.PlannedLiters
			case errors.Is(err, ErrActiveSession):
				busy++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if succeeded != 1 {
		t.Fatalf("succeeded=%d, want exactly one approved fill", succeeded)
	}
	if busy != workers-1 {
		t.Fatalf("busy=%d, want %d", busy, workers-1)
	}
	if available := profile.AvailableLiters(100, 0); approved > available {
		t.Fatalf("approved %.3f L exceeds available %.3f L", approved, available)
	}
	if held := coordinator.heldLiters("V-01"); held != approved {
		t.Fatalf("held=%.3f, want %.3f", held, approved)
	}
}

func TestHoldReleasedAfterAbort(t *testing.T) {
	profile := coordinatorTestProfile(t)
	coordinator, _ := newTestCoordinator(t, "V-01", 100, &profile)
	session, err := coordinator.StartManual(context.Background(), "V-01", 30)
	if err != nil {
		t.Fatal(err)
	}
	if held := coordinator.heldLiters("V-01"); held != 30 {
		t.Fatalf("held=%.3f", held)
	}
	aborts := NewAbortService(coordinator, time.Now)
	if _, err := aborts.Abort(context.Background(), session.ID, "test abort"); err != nil {
		t.Fatal(err)
	}
	if held := coordinator.heldLiters("V-01"); held != 0 {
		t.Fatalf("held=%.3f after abort", held)
	}
	// The released capacity is available to the next plan.
	if _, err := coordinator.StartManual(context.Background(), "V-01", 30); err != nil {
		t.Fatalf("capacity was not reusable after abort: %v", err)
	}
}
