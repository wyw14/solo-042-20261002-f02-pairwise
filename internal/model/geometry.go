package model

import (
	"errors"
	"math"
)

// mm2PerLiter converts cubic millimetres to litres (1 L = 1,000,000 mm³).
const mm2PerLiter = 1_000_000.0

// VesselGeometry describes the vertical cylindrical geometry of a vessel and
// the fill limits applied on top of it.
//
// Volumes are derived from level probe readings:
//
//	volume(litres) = level(mm) * cross_section(mm²) / 1_000_000
//
// Safe capacity never uses the whole tank: MaxFillRatio caps the usable fill
// height, and ReserveMM keeps an additional headspace band below that cap.
// MaxSingleLiters bounds a single fill session independently.
type VesselGeometry struct {
	HeightMM        float64 `json:"height_mm"`
	CrossSectionMM2 float64 `json:"cross_section_mm2"`
	MaxFillRatio    float64 `json:"max_fill_ratio"`
	MaxSingleLiters float64 `json:"max_single_liters"`
	ReserveMM       float64 `json:"reserve_mm"`
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func (g VesselGeometry) Validate() error {
	switch {
	case !finitePositive(g.HeightMM):
		return errors.New("geometry height_mm must be a positive finite number")
	case !finitePositive(g.CrossSectionMM2):
		return errors.New("geometry cross_section_mm2 must be a positive finite number")
	case math.IsNaN(g.MaxFillRatio) || math.IsInf(g.MaxFillRatio, 0) || g.MaxFillRatio <= 0 || g.MaxFillRatio > 1:
		return errors.New("geometry max_fill_ratio must be within (0, 1]")
	case !finitePositive(g.MaxSingleLiters):
		return errors.New("geometry max_single_liters must be a positive finite number")
	case math.IsNaN(g.ReserveMM) || math.IsInf(g.ReserveMM, 0) || g.ReserveMM < 0:
		return errors.New("geometry reserve_mm must be a non-negative finite number")
	}
	if g.ReserveMM >= g.SafeLevelMM() {
		return errors.New("geometry reserve_mm must leave room below the safe fill level")
	}
	if g.MaxSingleLiters > g.TotalLiters() {
		return errors.New("geometry max_single_liters must not exceed total vessel volume")
	}
	return nil
}

// TotalLiters is the full geometric volume of the vessel.
func (g VesselGeometry) TotalLiters() float64 {
	return g.HeightMM * g.CrossSectionMM2 / mm2PerLiter
}

// SafeLevelMM is the fill height allowed by MaxFillRatio.
func (g VesselGeometry) SafeLevelMM() float64 {
	return g.HeightMM * g.MaxFillRatio
}

// SafeLiters is the volume at the safe fill level.
func (g VesselGeometry) SafeLiters() float64 {
	return g.SafeLevelMM() * g.CrossSectionMM2 / mm2PerLiter
}

// ReserveLiters is the headspace volume kept free below the safe fill level.
func (g VesselGeometry) ReserveLiters() float64 {
	return g.ReserveMM * g.CrossSectionMM2 / mm2PerLiter
}

// ClampLevel constrains a probe reading to the measurable height band.
func (g VesselGeometry) ClampLevel(levelMM float64) float64 {
	if levelMM < 0 {
		return 0
	}
	if levelMM > g.HeightMM {
		return g.HeightMM
	}
	return levelMM
}

// VolumeLiters converts a level reading to litres, clamped to the vessel
// height.
func (g VesselGeometry) VolumeLiters(levelMM float64) float64 {
	return g.ClampLevel(levelMM) * g.CrossSectionMM2 / mm2PerLiter
}

// AvailableLiters returns the headspace that can still be planned, given the
// current level and litres already reserved by in-flight fill sessions. The
// result is never negative.
func (g VesselGeometry) AvailableLiters(currentLevelMM, reservedLiters float64) float64 {
	available := g.SafeLiters() - g.ReserveLiters() - g.VolumeLiters(currentLevelMM) - reservedLiters
	if available < 0 {
		return 0
	}
	return available
}
