package model

import (
	"testing"
	"time"
)

func TestFillSessionTransitionsThroughOperationalStates(t *testing.T) {
	now := time.Unix(100, 0)
	session, err := NewFillSession("V-01", FillManual, 1, 2.1, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []FillState{FillPrechecking, FillFilling, FillSoaking, FillVerified, FillCompleted} {
		session, err = session.Transition(state, now.Add(time.Second))
		if err != nil {
			t.Fatalf("transition to %s failed: %v", state, err)
		}
	}
	if session.Active() {
		t.Fatal("completed fill remained active")
	}
}

func TestAlarmChannelSummaryPreservesRequiredFailure(t *testing.T) {
	now := time.Unix(100, 0)
	alarm, err := NewAlarm("V-01", AlarmLeak, "leak", now)
	if err != nil {
		t.Fatal(err)
	}
	alarm = alarm.WithChannels([]ChannelResult{
		{Channel: "terminal", Required: true, Delivered: true},
		{Channel: "siren", Required: true, Delivered: false, Error: "offline"},
	}, now)
	if alarm.State != AlarmPartial {
		t.Fatalf("state=%s", alarm.State)
	}
}

func TestVesselCapacityValidation(t *testing.T) {
	if _, err := NewVesselCapacity("V-01", 0.5, 100, 40, 10); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	cases := []struct {
		name                                   string
		vesselID                               string
		litersPerMM, safe, maxSingle, reserved float64
	}{
		{"missing vessel id", "", 0.5, 100, 40, 10},
		{"zero conversion", "V-01", 0, 100, 40, 10},
		{"zero safe capacity", "V-01", 0.5, 0, 40, 10},
		{"negative reserved", "V-01", 0.5, 100, 40, -1},
		{"reserved consumes safe capacity", "V-01", 0.5, 100, 40, 100},
		{"zero single fill limit", "V-01", 0.5, 100, 0, 10},
		{"single fill limit exceeds usable capacity", "V-01", 0.5, 100, 95, 10},
	}
	for _, tc := range cases {
		if _, err := NewVesselCapacity(tc.vesselID, tc.litersPerMM, tc.safe, tc.maxSingle, tc.reserved); err == nil {
			t.Fatalf("%s: invalid profile accepted", tc.name)
		}
	}
}

func TestVesselCapacityVolumeAndAvailability(t *testing.T) {
	profile, err := NewVesselCapacity("V-01", 0.5, 100, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.VolumeAtLevel(100); got != 50 {
		t.Fatalf("volume at 100mm=%v", got)
	}
	if got := profile.VolumeAtLevel(-5); got != 0 {
		t.Fatalf("volume at negative level=%v", got)
	}
	// 100 safe - 10 reserved - 50 current - 5 held = 35 available.
	if got := profile.AvailableLiters(100, 5); got != 35 {
		t.Fatalf("available=%v", got)
	}
	// Never reports negative availability.
	if got := profile.AvailableLiters(500, 0); got != 0 {
		t.Fatalf("available=%v", got)
	}
}
