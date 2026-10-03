package vessel

import (
	"errors"
	"sort"
	"sync"

	"github.com/wyw14/cry-98/internal/model"
)

// CapacityRegistry holds the static per-vessel capacity profiles used to
// validate fill plans before a valve is opened.
type CapacityRegistry struct {
	mu       sync.RWMutex
	profiles map[string]model.VesselCapacity
}

func NewCapacityRegistry() *CapacityRegistry {
	return &CapacityRegistry{profiles: make(map[string]model.VesselCapacity)}
}

func (r *CapacityRegistry) Register(profile model.VesselCapacity) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.profiles[profile.VesselID]; exists {
		return errors.New("capacity profile already registered")
	}
	r.profiles[profile.VesselID] = profile
	return nil
}

func (r *CapacityRegistry) Get(vesselID string) (model.VesselCapacity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[vesselID]
	return profile, ok
}

func (r *CapacityRegistry) List() []model.VesselCapacity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]model.VesselCapacity, 0, len(r.profiles))
	for _, profile := range r.profiles {
		result = append(result, profile)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].VesselID < result[j].VesselID })
	return result
}
