package model

import (
	"errors"
	"math"
	"strings"
	"time"
)

type VesselStatus string

const (
	VesselOffline VesselStatus = "offline"
	VesselReady   VesselStatus = "ready"
	VesselFilling VesselStatus = "filling"
	VesselFault   VesselStatus = "fault"
)

type Vessel struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Zone         string          `json:"zone"`
	Generation   uint64          `json:"generation"`
	Status       VesselStatus    `json:"status"`
	LevelMM      float64         `json:"level_mm"`
	LevelKnown   bool            `json:"level_known"`
	PressureKPa  float64         `json:"pressure_kpa"`
	TemperatureC float64         `json:"temperature_c"`
	Geometry     *VesselGeometry `json:"geometry,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func NewVessel(id, name, zone string, now time.Time) (Vessel, error) {
	return NewVesselWithGeometry(id, name, zone, nil, now)
}

func NewVesselWithGeometry(id, name, zone string, geometry *VesselGeometry, now time.Time) (Vessel, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	zone = strings.TrimSpace(zone)
	if id == "" || name == "" || zone == "" {
		return Vessel{}, errors.New("vessel id, name, and zone are required")
	}
	if geometry != nil {
		if err := geometry.Validate(); err != nil {
			return Vessel{}, err
		}
	}
	return Vessel{
		ID:         id,
		Name:       name,
		Zone:       zone,
		Generation: 1,
		Status:     VesselReady,
		Geometry:   geometry,
		UpdatedAt:  now.UTC(),
	}, nil
}

func (v Vessel) Validate() error {
	if strings.TrimSpace(v.ID) == "" {
		return errors.New("vessel id is required")
	}
	if v.Generation == 0 {
		return errors.New("vessel generation must be positive")
	}
	switch v.Status {
	case VesselOffline, VesselReady, VesselFilling, VesselFault:
	default:
		return errors.New("unknown vessel status")
	}
	if v.Geometry != nil {
		if err := v.Geometry.Validate(); err != nil {
			return err
		}
		if v.LevelKnown && (v.LevelMM < 0 || v.LevelMM > v.Geometry.HeightMM) {
			return errors.New("vessel level is outside the configured geometry")
		}
	}
	return nil
}

// ConfigureGeometry attaches (or replaces) the vessel geometry. A known level
// is re-clamped against the new geometry.
func (v Vessel) ConfigureGeometry(geometry VesselGeometry, now time.Time) (Vessel, error) {
	if err := geometry.Validate(); err != nil {
		return Vessel{}, err
	}
	v.Geometry = &geometry
	if v.LevelKnown {
		v.LevelMM = geometry.ClampLevel(v.LevelMM)
	}
	v.UpdatedAt = now.UTC()
	return v, nil
}

func (v Vessel) WithStatus(status VesselStatus, now time.Time) Vessel {
	v.Status = status
	v.UpdatedAt = now.UTC()
	return v
}

func (v Vessel) WithTelemetry(level, pressure, temperature float64, now time.Time) Vessel {
	if v.Geometry != nil {
		level = v.Geometry.ClampLevel(level)
	}
	v.LevelMM = level
	v.LevelKnown = true
	v.PressureKPa = pressure
	v.TemperatureC = temperature
	v.UpdatedAt = now.UTC()
	return v
}

// WithAddedVolume advances the modelled level by the delivered litres,
// converting through the configured geometry. It is used when a fill completes
// before fresh level telemetry has arrived, so the reserved capacity is not
// released against a stale level. Real probe readings override it afterwards
// via WithTelemetry.
func (v Vessel) WithAddedVolume(liters float64, now time.Time) (Vessel, error) {
	if v.Geometry == nil {
		return Vessel{}, errors.New("vessel geometry is not configured")
	}
	if math.IsNaN(liters) || math.IsInf(liters, 0) || liters < 0 {
		return Vessel{}, errors.New("added volume must be a non-negative finite number")
	}
	deltaMM := liters * mm2PerLiter / v.Geometry.CrossSectionMM2
	v.LevelMM = v.Geometry.ClampLevel(v.LevelMM + deltaMM)
	v.LevelKnown = true
	v.UpdatedAt = now.UTC()
	return v, nil
}
