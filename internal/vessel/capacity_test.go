package vessel

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-98/internal/model"
)

func newCapacityHarness(t *testing.T, levelKnown bool, levelMM float64) (*Registry, *StateService, *CapacityService) {
	t.Helper()
	now := time.Now
	geometry := model.VesselGeometry{
		HeightMM:        1000,
		CrossSectionMM2: 100000, // 100 L total
		MaxFillRatio:    0.9,    // safe 90 L
		MaxSingleLiters: 30,
		ReserveMM:       100, // 10 L reserve band -> usable ceiling 80 L
	}
	v, err := model.NewVesselWithGeometry("V-01", "Vessel", "north", &geometry, now())
	if err != nil {
		t.Fatal(err)
	}
	if levelKnown {
		v = v.WithTelemetry(levelMM, 0, 0, now())
	}
	registry := NewRegistry()
	if err := registry.Register(v); err != nil {
		t.Fatal(err)
	}
	states := NewStateService(registry, now)
	return registry, states, NewCapacityService(states)
}

func TestCapacityAvailableComputation(t *testing.T) {
	// 500 mm = 50 L; ceiling 80 L -> 30 L available.
	_, _, capacity := newCapacityHarness(t, true, 500)
	available, err := capacity.AvailableLiters("V-01")
	if err != nil {
		t.Fatal(err)
	}
	if available != 30 {
		t.Fatalf("available = %v, want 30", available)
	}
}

func TestCapacityPlanRejections(t *testing.T) {
	_, _, capacity := newCapacityHarness(t, true, 500)

	if err := capacity.PlanFill("V-01", uuid.New(), 0); !errors.Is(err, ErrInvalidFillVolume) {
		t.Fatalf("zero litres: %v", err)
	}
	if err := capacity.PlanFill("V-01", uuid.New(), -3); !errors.Is(err, ErrInvalidFillVolume) {
		t.Fatalf("negative litres: %v", err)
	}
	if err := capacity.PlanFill("V-01", uuid.New(), math.NaN()); !errors.Is(err, ErrInvalidFillVolume) {
		t.Fatalf("NaN litres: %v", err)
	}
	// 31 L > 30 L single-fill cap, while 30 L would otherwise fit.
	if err := capacity.PlanFill("V-01", uuid.New(), 31); !errors.Is(err, ErrExceedsSingleFill) {
		t.Fatalf("single-fill cap not enforced: %v", err)
	}
	// 30 L fits now; after it is reserved, a second 5 L plan has only 0 L
	// (single-fill cap aside) and must be refused for capacity.
	first := uuid.New()
	if err := capacity.PlanFill("V-01", first, 30); err != nil {
		t.Fatalf("first plan should succeed: %v", err)
	}
	err := capacity.PlanFill("V-01", uuid.New(), 5)
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("over-capacity plan: %v", err)
	}
	// Duplicate reservation for the same session.
	if err := capacity.PlanFill("V-01", first, 1); !errors.Is(err, ErrDuplicateReservation) {
		t.Fatalf("duplicate reservation: %v", err)
	}
}

func TestCapacityRejectsUnknownLevelAndMissingGeometry(t *testing.T) {
	// No telemetry yet.
	_, _, unknown := newCapacityHarness(t, false, 0)
	if err := unknown.PlanFill("V-01", uuid.New(), 10); !errors.Is(err, ErrLevelUnknown) {
		t.Fatalf("unknown level: %v", err)
	}

	now := time.Now
	plain, _ := model.NewVessel("V-99", "Plain", "north", now())
	registry := NewRegistry()
	if err := registry.Register(plain); err != nil {
		t.Fatal(err)
	}
	capacity := NewCapacityService(NewStateService(registry, now))
	if err := capacity.PlanFill("V-99", uuid.New(), 10); !errors.Is(err, ErrGeometryNotConfigured) {
		t.Fatalf("missing geometry: %v", err)
	}
}

func TestCapacityReleaseMakesHeadspaceAvailableAgain(t *testing.T) {
	_, _, capacity := newCapacityHarness(t, true, 500)
	session := uuid.New()
	if err := capacity.PlanFill("V-01", session, 30); err != nil {
		t.Fatal(err)
	}
	if available, _ := capacity.AvailableLiters("V-01"); available != 0 {
		t.Fatalf("available after reservation = %v, want 0", available)
	}
	capacity.ReleaseFill("V-01", session)
	if available, _ := capacity.AvailableLiters("V-01"); available != 30 {
		t.Fatalf("available after release = %v, want 30", available)
	}
	// Releasing an unknown reservation is a no-op.
	capacity.ReleaseFill("V-01", uuid.New())
}

func TestCapacityCommitAdvancesLevel(t *testing.T) {
	registry, _, capacity := newCapacityHarness(t, true, 500)
	session := uuid.New()
	if err := capacity.PlanFill("V-01", session, 20); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := capacity.CommitFill("V-01", session, now); err != nil {
		t.Fatal(err)
	}
	v, _ := registry.Get("V-01")
	// 20 L over 0.1 m² = 200 mm.
	if v.LevelMM != 700 {
		t.Fatalf("level after commit = %v, want 700", v.LevelMM)
	}
	if available, _ := capacity.AvailableLiters("V-01"); available != 10 {
		t.Fatalf("available after commit = %v, want 10", available)
	}
	// Commit is idempotent: no second level jump.
	if err := capacity.CommitFill("V-01", session, now); err != nil {
		t.Fatal(err)
	}
	v, _ = registry.Get("V-01")
	if v.LevelMM != 700 {
		t.Fatalf("level after second commit = %v, want 700", v.LevelMM)
	}
}

// TestCapacityConcurrentPlansCannotShareHeadspace fires many concurrent plans
// whose total exceeds the 30 L available. The accepted plans' volumes must
// never exceed the available headspace.
func TestCapacityConcurrentPlansCannotShareHeadspace(t *testing.T) {
	_, _, capacity := newCapacityHarness(t, true, 500)

	const attempts = 64
	const each = 3.0 // 64 * 3 L = 192 L requested, only 30 L available

	var wg sync.WaitGroup
	results := make(chan float64, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			granted := 0.0
			if err := capacity.PlanFill("V-01", uuid.New(), each); err == nil {
				granted = each
			}
			results <- granted
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var grantedTotal float64
	var grantedCount int
	for granted := range results {
		grantedTotal += granted
		if granted > 0 {
			grantedCount++
		}
	}
	if grantedTotal > 30 {
		t.Fatalf("granted %v L across concurrent plans, available was 30 L", grantedTotal)
	}
	if grantedCount != 10 {
		t.Fatalf("granted %d plans of 3 L, want exactly 10", grantedCount)
	}
	if available, _ := capacity.AvailableLiters("V-01"); available != 0 {
		t.Fatalf("residual available = %v, want 0", available)
	}
}
