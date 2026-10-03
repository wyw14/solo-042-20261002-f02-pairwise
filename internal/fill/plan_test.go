package fill

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/cry-98/internal/model"
)

func planTestProfile(t *testing.T) model.VesselCapacity {
	t.Helper()
	profile, err := model.NewVesselCapacity("V-01", 0.5, 100, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func planTestVessel(t *testing.T, levelMM float64) model.Vessel {
	t.Helper()
	v, err := model.NewVessel("V-01", "test vessel", "test", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	return v.WithTelemetry(levelMM, 100, -180, time.Unix(101, 0))
}

func TestValidatePlanRejectsNonPositiveAndNonFinite(t *testing.T) {
	profile := planTestProfile(t)
	current := planTestVessel(t, 100)
	for _, liters := range []float64{0, -5, math.NaN(), math.Inf(1)} {
		rejection := validatePlan("V-01", liters, current, profile, 0)
		if rejection == nil {
			t.Fatalf("liters=%v was accepted", liters)
		}
		if rejection.Reason != ReasonInvalidPlannedLiters {
			t.Fatalf("liters=%v reason=%s", liters, rejection.Reason)
		}
	}
}

func TestValidatePlanEnforcesSingleFillLimit(t *testing.T) {
	profile := planTestProfile(t)
	current := planTestVessel(t, 100)
	if rejection := validatePlan("V-01", 40, current, profile, 0); rejection != nil {
		t.Fatalf("planned liters equal to the limit were rejected: %v", rejection)
	}
	rejection := validatePlan("V-01", 40.5, current, profile, 0)
	if rejection == nil {
		t.Fatal("planned liters above the single fill limit were accepted")
	}
	if rejection.Reason != ReasonExceedsSingleFillLimit {
		t.Fatalf("reason=%s", rejection.Reason)
	}
	if rejection.Bound != 40 {
		t.Fatalf("bound=%v", rejection.Bound)
	}
}

func TestValidatePlanConvertsLevelAndSubtractsReservedAndHeld(t *testing.T) {
	// Single fill limit sits above what the vessel can currently take, so the
	// available-capacity check is what bites.
	profile, err := model.NewVesselCapacity("V-01", 0.5, 100, 90, 10)
	if err != nil {
		t.Fatal(err)
	}
	// level 100mm * 0.5 L/mm = 50 L current; available = 100 - 10 - 50 = 40 L.
	current := planTestVessel(t, 100)
	if rejection := validatePlan("V-01", 40, current, profile, 0); rejection != nil {
		t.Fatalf("planned liters equal to available were rejected: %v", rejection)
	}
	rejection := validatePlan("V-01", 40.5, current, profile, 0)
	if rejection == nil {
		t.Fatal("planned liters above available capacity were accepted")
	}
	if rejection.Reason != ReasonInsufficientCapacity {
		t.Fatalf("reason=%s", rejection.Reason)
	}
	if rejection.Bound != 40 {
		t.Fatalf("bound=%v", rejection.Bound)
	}
	// Held reservations shrink what is left for the next plan.
	if rejection := validatePlan("V-01", 30, current, profile, 10); rejection != nil {
		t.Fatalf("planned liters equal to available minus held were rejected: %v", rejection)
	}
	if rejection := validatePlan("V-01", 30.5, current, profile, 10); rejection == nil {
		t.Fatal("held liters were not subtracted from available capacity")
	} else if rejection.Bound != 30 {
		t.Fatalf("bound=%v", rejection.Bound)
	}
}

func TestValidatePlanRejectsFullVessel(t *testing.T) {
	profile := planTestProfile(t)
	// level 180mm * 0.5 L/mm = 90 L current; available = 100 - 10 - 90 = 0 L.
	current := planTestVessel(t, 180)
	rejection := validatePlan("V-01", 1, current, profile, 0)
	if rejection == nil {
		t.Fatal("fill into a full vessel was accepted")
	}
	if rejection.Reason != ReasonInsufficientCapacity {
		t.Fatalf("reason=%s", rejection.Reason)
	}
	if rejection.Bound != 0 {
		t.Fatalf("bound=%v", rejection.Bound)
	}
}
