package model

import (
	"math"
	"testing"
	"time"
)

func testGeometry() VesselGeometry {
	// 1000 mm tall, 0.1 m² cross section => 100 L total.
	return VesselGeometry{
		HeightMM:        1000,
		CrossSectionMM2: 100000,
		MaxFillRatio:    0.9,
		MaxSingleLiters: 30,
		ReserveMM:       100,
	}
}

func TestGeometryConversions(t *testing.T) {
	g := testGeometry()
	if got := g.TotalLiters(); got != 100 {
		t.Fatalf("total litres = %v, want 100", got)
	}
	if got := g.SafeLevelMM(); got != 900 {
		t.Fatalf("safe level = %v, want 900", got)
	}
	if got := g.SafeLiters(); got != 90 {
		t.Fatalf("safe litres = %v, want 90", got)
	}
	if got := g.ReserveLiters(); got != 10 {
		t.Fatalf("reserve litres = %v, want 10", got)
	}
	// Level 500 mm = 50 L; usable headspace = 90 - 10 - 50 = 30 L.
	if got := g.VolumeLiters(500); got != 50 {
		t.Fatalf("volume at 500 mm = %v, want 50", got)
	}
	if got := g.AvailableLiters(500, 0); got != 30 {
		t.Fatalf("available at 500 mm = %v, want 30", got)
	}
	// An existing reservation reduces available capacity.
	if got := g.AvailableLiters(500, 10); got != 20 {
		t.Fatalf("available with 10 L reserved = %v, want 20", got)
	}
	// Never negative even when level is already above the ceiling.
	if got := g.AvailableLiters(900, 5); got != 0 {
		t.Fatalf("oversubscribed available = %v, want 0", got)
	}
}

func TestGeometryClampLevel(t *testing.T) {
	g := testGeometry()
	if got := g.ClampLevel(-5); got != 0 {
		t.Fatalf("negative level clamped to %v, want 0", got)
	}
	if got := g.ClampLevel(5000); got != 1000 {
		t.Fatalf("overheight level clamped to %v, want 1000", got)
	}
	if got := g.VolumeLiters(math.NaN()); !math.IsNaN(got) {
		t.Fatal("NaN level should propagate, not panic")
	}
}

func TestGeometryValidation(t *testing.T) {
	cases := map[string]func(VesselGeometry) VesselGeometry{
		"height":                func(g VesselGeometry) VesselGeometry { g.HeightMM = 0; return g },
		"cross section":         func(g VesselGeometry) VesselGeometry { g.CrossSectionMM2 = -1; return g },
		"fill ratio zero":       func(g VesselGeometry) VesselGeometry { g.MaxFillRatio = 0; return g },
		"fill ratio above one":  func(g VesselGeometry) VesselGeometry { g.MaxFillRatio = 1.1; return g },
		"single fill":           func(g VesselGeometry) VesselGeometry { g.MaxSingleLiters = 0; return g },
		"reserve negative":      func(g VesselGeometry) VesselGeometry { g.ReserveMM = -1; return g },
		"reserve above safe":    func(g VesselGeometry) VesselGeometry { g.ReserveMM = 950; return g },
		"single above capacity": func(g VesselGeometry) VesselGeometry { g.MaxSingleLiters = 200; return g },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := mutate(testGeometry()).Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if err := testGeometry().Validate(); err != nil {
		t.Fatalf("valid geometry rejected: %v", err)
	}
}

func TestVesselTelemetryClampsAndMarksLevelKnown(t *testing.T) {
	now := time.Now()
	v, err := NewVesselWithGeometry("V-01", "Vessel", "north", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.LevelKnown {
		t.Fatal("new vessel should not report a known level")
	}
	g := testGeometry()
	v, err = v.ConfigureGeometry(g, now)
	if err != nil {
		t.Fatal(err)
	}
	v = v.WithTelemetry(2000, 0, 0, now)
	if v.LevelMM != 1000 {
		t.Fatalf("telemetry level not clamped: %v", v.LevelMM)
	}
	if !v.LevelKnown {
		t.Fatal("telemetry should mark level known")
	}
}

func TestVesselRejectsLevelOutsideGeometry(t *testing.T) {
	g := testGeometry()
	v, err := NewVesselWithGeometry("V-01", "Vessel", "north", &g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v = v.WithTelemetry(500, 0, 0, time.Now())
	v.LevelMM = 5000
	if err := v.Validate(); err == nil {
		t.Fatal("vessel above geometry height should fail validation")
	}
}
