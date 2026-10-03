package model

import (
	"errors"
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
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Zone         string       `json:"zone"`
	Generation   uint64       `json:"generation"`
	Status       VesselStatus `json:"status"`
	LevelMM      float64      `json:"level_mm"`
	PressureKPa  float64      `json:"pressure_kpa"`
	TemperatureC float64      `json:"temperature_c"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

func NewVessel(id, name, zone string, now time.Time) (Vessel, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	zone = strings.TrimSpace(zone)
	if id == "" || name == "" || zone == "" {
		return Vessel{}, errors.New("vessel id, name, and zone are required")
	}
	return Vessel{
		ID:         id,
		Name:       name,
		Zone:       zone,
		Generation: 1,
		Status:     VesselReady,
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
		return nil
	default:
		return errors.New("unknown vessel status")
	}
}

func (v Vessel) WithStatus(status VesselStatus, now time.Time) Vessel {
	v.Status = status
	v.UpdatedAt = now.UTC()
	return v
}

func (v Vessel) WithTelemetry(level, pressure, temperature float64, now time.Time) Vessel {
	v.LevelMM = level
	v.PressureKPa = pressure
	v.TemperatureC = temperature
	v.UpdatedAt = now.UTC()
	return v
}

// VesselCapacity is the static per-vessel configuration used to validate fill
// plans: how a level reading converts to stored volume, the safe capacity, the
// largest single fill, and the headspace that must remain free.
type VesselCapacity struct {
	VesselID            string  `json:"vessel_id"`
	LitersPerMM         float64 `json:"liters_per_mm"`
	SafeCapacityLiters  float64 `json:"safe_capacity_liters"`
	MaxSingleFillLiters float64 `json:"max_single_fill_liters"`
	ReservedLiters      float64 `json:"reserved_liters"`
}

func NewVesselCapacity(vesselID string, litersPerMM, safeCapacity, maxSingleFill, reserved float64) (VesselCapacity, error) {
	profile := VesselCapacity{
		VesselID:            strings.TrimSpace(vesselID),
		LitersPerMM:         litersPerMM,
		SafeCapacityLiters:  safeCapacity,
		MaxSingleFillLiters: maxSingleFill,
		ReservedLiters:      reserved,
	}
	if err := profile.Validate(); err != nil {
		return VesselCapacity{}, err
	}
	return profile, nil
}

func (c VesselCapacity) Validate() error {
	if strings.TrimSpace(c.VesselID) == "" {
		return errors.New("vessel id is required")
	}
	if c.LitersPerMM <= 0 {
		return errors.New("liters per millimeter must be positive")
	}
	if c.SafeCapacityLiters <= 0 {
		return errors.New("safe capacity must be positive")
	}
	if c.ReservedLiters < 0 || c.ReservedLiters >= c.SafeCapacityLiters {
		return errors.New("reserved liters must be within [0, safe capacity)")
	}
	if c.MaxSingleFillLiters <= 0 {
		return errors.New("single fill limit must be positive")
	}
	if c.MaxSingleFillLiters > c.SafeCapacityLiters-c.ReservedLiters {
		return errors.New("single fill limit exceeds usable capacity")
	}
	return nil
}

// VolumeAtLevel converts a level reading in millimeters to stored volume in liters.
func (c VesselCapacity) VolumeAtLevel(levelMM float64) float64 {
	if levelMM <= 0 {
		return 0
	}
	return levelMM * c.LitersPerMM
}

// AvailableLiters returns how much additional volume may still be planned:
// safe capacity minus reserved headspace, current contents, and liters already
// held by active fill sessions.
func (c VesselCapacity) AvailableLiters(levelMM, heldLiters float64) float64 {
	available := c.SafeCapacityLiters - c.ReservedLiters - c.VolumeAtLevel(levelMM) - heldLiters
	if available < 0 {
		return 0
	}
	return available
}
