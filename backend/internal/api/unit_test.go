package api

import (
	"testing"
	"time"
)

// Pure helpers of the api package; these run without a database.

func TestDriverEventRules(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	dep := now.Add(-time.Hour)
	temp := 400
	cases := []struct {
		name    string
		ev      driverEvent
		status  string
		outcome string
	}{
		{"delivery in transit", driverEvent{Kind: "delivery", RecordedAt: now}, "in_transit", "applied"},
		{"second delivery", driverEvent{Kind: "delivery", RecordedAt: now}, "delivered", "duplicate_delivery"},
		{"not departed yet", driverEvent{Kind: "delivery", RecordedAt: now}, "loading", "rejected"},
		{"before departure", driverEvent{Kind: "delivery", RecordedAt: dep.Add(-time.Minute)}, "in_transit", "rejected"},
		{"phone clock far ahead", driverEvent{Kind: "delivery", RecordedAt: now.Add(time.Hour)}, "in_transit", "rejected"},
		{"temperature check", driverEvent{Kind: "temperature_check", RecordedAt: now, TempCC: &temp}, "delivered", "applied"},
		{"temperature without value", driverEvent{Kind: "temperature_check", RecordedAt: now}, "in_transit", "rejected"},
		{"unknown kind", driverEvent{Kind: "pickup", RecordedAt: now}, "in_transit", "rejected"},
	}
	for _, c := range cases {
		if got, _ := classifyDriverEvent(c.ev, c.status, &dep, now); got != c.outcome {
			t.Errorf("%s: got %s, want %s", c.name, got, c.outcome)
		}
	}
}

func TestDriverEventsAppliedInRecordedOrder(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evs := []driverEvent{{RecordedAt: base.Add(2 * time.Minute)}, {RecordedAt: base}, {RecordedAt: base.Add(time.Minute)}}
	sortEvents(evs)
	for i := 1; i < len(evs); i++ {
		if evs[i].RecordedAt.Before(evs[i-1].RecordedAt) {
			t.Fatal("not sorted")
		}
	}
}

func TestDurationFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:                "45 sn",
		8*time.Minute + 26*time.Second:  "8 dk 26 sn",
		10 * time.Minute:                "10 dk",
		time.Hour + 5*time.Minute + 3e9: "1 sa 5 dk",
	} {
		if got := dur(d); got != want {
			t.Errorf("dur(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestResolutionChoice(t *testing.T) {
	for span, want := range map[time.Duration]string{
		time.Hour: "raw", 24 * time.Hour: "1m", 7 * 24 * time.Hour: "15m", 90 * 24 * time.Hour: "1h",
	} {
		if got := resolutionFor(span); got != want {
			t.Errorf("%s: %s", span, got)
		}
	}
}

func TestQualityStateMachineNeverUndoes(t *testing.T) {
	for _, terminal := range []string{"destroyed", "recalled"} {
		if len(allowedDecisions[terminal]) != 0 {
			t.Errorf("%s must be final", terminal)
		}
	}
	for _, d := range allowedDecisions["discounted"] {
		if d == "accept" {
			t.Error("a discounted lot cannot become accepted again")
		}
	}
}
