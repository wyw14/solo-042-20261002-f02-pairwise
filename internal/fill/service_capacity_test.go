package fill

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/circuit"
	"github.com/wyw14/cry-98/internal/model"
	"github.com/wyw14/cry-98/internal/valve"
	"github.com/wyw14/cry-98/internal/vessel"
)

type fillHarness struct {
	registry *vessel.Registry
	capacity *vessel.CapacityService
	routes   *valve.RouteService
	fills    *Service
}

func newFillHarness(t *testing.T) fillHarness {
	t.Helper()
	now := time.Now
	// 1000 mm / 0.1 m² = 100 L; safe 90 L; reserve band 10 L; level 500 mm
	// (50 L) -> 30 L available; single-fill cap deliberately high so that
	// capacity, not the per-session cap, is the binding limit here.
	geometry := model.VesselGeometry{
		HeightMM:        1000,
		CrossSectionMM2: 100000,
		MaxFillRatio:    0.9,
		MaxSingleLiters: 90,
		ReserveMM:       100,
	}
	v, err := model.NewVesselWithGeometry("V-01", "Vessel", "north", &geometry, now())
	if err != nil {
		t.Fatal(err)
	}
	v = v.WithTelemetry(500, 0, 0, now())
	registry := vessel.NewRegistry()
	if err := registry.Register(v); err != nil {
		t.Fatal(err)
	}
	// Second vessel nearly at the 80 L usable ceiling: 790 mm = 79 L, so only
	// 1 L remains for new plans while the automatic default is 2.1 L.
	nearFull, err := model.NewVesselWithGeometry("V-02", "Vessel two", "north", &geometry, now())
	if err != nil {
		t.Fatal(err)
	}
	nearFull = nearFull.WithTelemetry(790, 0, 0, now())
	if err := registry.Register(nearFull); err != nil {
		t.Fatal(err)
	}
	states := vessel.NewStateService(registry, now)
	capacity := vessel.NewCapacityService(states)

	circuitRegistry := circuit.NewRegistry(circuit.Circuit{ID: "C-A", Zone: "north", ValveID: "VALVE-A", Enabled: true})
	circuitManager := circuit.NewManager("C-A")
	transport := valve.NewMemoryTransport(now)
	controller, err := valve.NewController(transport, now)
	if err != nil {
		t.Fatal(err)
	}
	routes := valve.NewRouteService(valve.NewCommandService(controller, now))
	coordinator := NewCoordinator(circuitManager, circuitRegistry, routes, states, capacity, now)
	aborts := NewAbortService(coordinator, now)
	service, err := NewService(coordinator, nil, aborts, 2.1)
	if err != nil {
		t.Fatal(err)
	}
	return fillHarness{registry: registry, capacity: capacity, routes: routes, fills: service}
}

func TestManualRejectsNonPositiveVolume(t *testing.T) {
	h := newFillHarness(t)
	for _, liters := range []float64{0, -1} {
		if _, err := h.fills.StartManual(context.Background(), "V-01", liters); !errors.Is(err, vessel.ErrInvalidFillVolume) {
			t.Fatalf("StartManual(%v) error = %v, want ErrInvalidFillVolume", liters, err)
		}
	}
}

func TestOverCapacityPlanRejectedBeforeValveOpens(t *testing.T) {
	h := newFillHarness(t)
	// 40 L requested, only 30 L available.
	if _, err := h.fills.StartManual(context.Background(), "V-01", 40); !errors.Is(err, vessel.ErrInsufficientCapacity) {
		t.Fatalf("StartManual error = %v, want ErrInsufficientCapacity", err)
	}
	// No session registered, no circuit consumed.
	if sessions := h.fills.List(); len(sessions) != 0 {
		t.Fatalf("rejected plan left %d sessions", len(sessions))
	}
	if _, busy := h.capacity.AvailableLiters("V-01"); busy != nil {
		t.Fatal(busy)
	}
	if available, _ := h.capacity.AvailableLiters("V-01"); available != 30 {
		t.Fatalf("available after rejection = %v, want 30 (reservation leaked)", available)
	}
}

func TestAbortReleasesReservedCapacity(t *testing.T) {
	h := newFillHarness(t)
	session, err := h.fills.StartManual(context.Background(), "V-01", 30)
	if err != nil {
		t.Fatal(err)
	}
	if available, _ := h.capacity.AvailableLiters("V-01"); available != 0 {
		t.Fatalf("available while filling = %v, want 0", available)
	}
	// Second request cannot reuse the same headspace (or the vessel).
	if _, err := h.fills.StartManual(context.Background(), "V-01", 1); !errors.Is(err, ErrActiveSession) {
		t.Fatalf("second active fill error = %v, want ErrActiveSession", err)
	}
	if _, err := h.fills.Abort(context.Background(), session.ID, "operator cancelled"); err != nil {
		t.Fatal(err)
	}
	if available, _ := h.capacity.AvailableLiters("V-01"); available != 30 {
		t.Fatalf("available after abort = %v, want 30", available)
	}
	// Route is closed after abort.
	if err := h.routes.Close(context.Background(), session.ID); err == nil {
		t.Fatal("aborted session left the route open")
	}
}

func TestAutomaticPlanIsAlsoCapacityChecked(t *testing.T) {
	h := newFillHarness(t)
	// V-02 sits at 79 L with an 80 L usable ceiling; the 2.1 L automatic
	// default must be refused on capacity before any valve opens.
	if _, err := h.fills.OnLowLevel(context.Background(), "V-02", time.Now()); !errors.Is(err, vessel.ErrInsufficientCapacity) {
		t.Fatalf("automatic start error = %v, want ErrInsufficientCapacity", err)
	}
	if sessions := h.fills.List(); len(sessions) != 0 {
		t.Fatalf("rejected automatic plan left %d sessions", len(sessions))
	}
}
