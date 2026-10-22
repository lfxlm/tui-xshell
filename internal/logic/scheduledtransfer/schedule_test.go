package scheduledtransfer

import (
	"testing"
	"time"
)

func TestValidateSchedule(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want bool
	}{
		{name: "standard cron", spec: "*/5 * * * *", want: true},
		{name: "descriptor", spec: "@hourly", want: true},
		{name: "every minute", spec: "@every 1m", want: true},
		{name: "short interval", spec: "@every 30s"},
		{name: "malformed interval", spec: "@every tomorrow"},
		{name: "invalid cron", spec: "61 * * * *"},
		{name: "empty", spec: " "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSchedule(test.spec)
			if (err == nil) != test.want {
				t.Fatalf("ValidateSchedule(%q) error = %v, want valid=%t", test.spec, err, test.want)
			}
		})
	}
}

func TestNextRun(t *testing.T) {
	after := time.Date(2026, time.October, 20, 8, 42, 17, 0, time.UTC)
	got, err := NextRun("* * * * *", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.October, 20, 8, 43, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextRun = %s, want %s", got, want)
	}
	if _, err := NextRun("@every 30s", after); err == nil {
		t.Fatal("NextRun accepted an interval shorter than the minimum")
	}
}

func TestNextRunInLocation(t *testing.T) {
	after := time.Date(2026, time.October, 20, 0, 0, 0, 0, time.UTC)
	got, err := NextRunInLocation("0 9 * * *", "Asia/Shanghai", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.October, 20, 9, 0, 0, 0, mustLoadTestLocation(t, "Asia/Shanghai"))
	if !got.Equal(want) || got.Location().String() != "Asia/Shanghai" {
		t.Fatalf("next run = %s (%s), want %s (%s)", got, got.Location(), want, want.Location())
	}
	if _, err := NextRunInLocation("0 9 * * *", "Not/A_Real_Zone", after); err == nil {
		t.Fatal("accepted an unknown timezone")
	}
	interval, err := NextRunInLocation("@every 30m", "Asia/Shanghai", after)
	if err != nil || !interval.Equal(after.Add(30*time.Minute)) {
		t.Fatalf("@every next run = %s, err=%v; want fixed interval %s", interval, err, after.Add(30*time.Minute))
	}
}

func mustLoadTestLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}
