package fill

import (
	"fmt"
	"math"

	"github.com/wyw14/cry-98/internal/model"
)

// RejectionReason identifies why a fill plan was refused, so callers and
// operators can act on a stable code instead of parsing message text.
type RejectionReason string

const (
	ReasonVesselUnknown          RejectionReason = "vessel_unknown"
	ReasonCapacityProfileMissing RejectionReason = "capacity_profile_missing"
	ReasonInvalidPlannedLiters   RejectionReason = "invalid_planned_liters"
	ReasonExceedsSingleFillLimit RejectionReason = "exceeds_single_fill_limit"
	ReasonInsufficientCapacity   RejectionReason = "insufficient_available_capacity"
)

// PlanRejection describes a refused fill plan. Bound carries the limit that
// was violated: the single-fill limit or the currently available liters.
type PlanRejection struct {
	Reason   RejectionReason
	VesselID string
	Planned  float64
	Bound    float64
	Detail   string
}

func (e *PlanRejection) Error() string {
	return fmt.Sprintf("fill plan rejected (%s): %s", e.Reason, e.Detail)
}

// validatePlan checks a planned fill volume against the vessel's capacity
// profile, its current level, and liters already held by active sessions.
// heldLiters must be the reservations collected under the coordinator lock so
// concurrent starts cannot approve the same available capacity twice.
func validatePlan(vesselID string, planned float64, current model.Vessel, profile model.VesselCapacity, heldLiters float64) *PlanRejection {
	if planned <= 0 || math.IsNaN(planned) || math.IsInf(planned, 0) {
		return &PlanRejection{
			Reason:   ReasonInvalidPlannedLiters,
			VesselID: vesselID,
			Planned:  planned,
			Detail:   fmt.Sprintf("planned liters %.3f for vessel %s must be a positive finite value", planned, vesselID),
		}
	}
	if planned > profile.MaxSingleFillLiters {
		return &PlanRejection{
			Reason:   ReasonExceedsSingleFillLimit,
			VesselID: vesselID,
			Planned:  planned,
			Bound:    profile.MaxSingleFillLiters,
			Detail: fmt.Sprintf("planned %.3f L exceeds single fill limit %.3f L for vessel %s",
				planned, profile.MaxSingleFillLiters, vesselID),
		}
	}
	available := profile.AvailableLiters(current.LevelMM, heldLiters)
	if planned > available {
		return &PlanRejection{
			Reason:   ReasonInsufficientCapacity,
			VesselID: vesselID,
			Planned:  planned,
			Bound:    available,
			Detail: fmt.Sprintf("planned %.3f L exceeds available %.3f L for vessel %s (current %.3f L at %.1f mm, safe capacity %.3f L, reserved %.3f L, held %.3f L)",
				planned, available, vesselID, profile.VolumeAtLevel(current.LevelMM), current.LevelMM,
				profile.SafeCapacityLiters, profile.ReservedLiters, heldLiters),
		}
	}
	return nil
}
