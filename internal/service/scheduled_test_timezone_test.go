package service

// Independent examples for docs/scheduled-tests-daily-timezone-contract.md.
import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func scheduledInstant(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestScheduledDailySchemaDefinition(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(scheduledTestPlanDDL); err != nil {
		t.Fatal(err)
	}
	for _, definition := range scheduledDailyColumnDDL {
		if _, err := db.Exec(`ALTER TABLE scheduled_test_plans ADD COLUMN ` + definition); err != nil {
			t.Fatal(err)
		}
	}
	var actual string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='scheduled_test_plans'`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(scheduledTestPlanFinalDDL(), "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	if normalizeHealthDDL(actual) != normalizeHealthDDL(expected) {
		t.Fatalf("actual %q; expected %q", normalizeHealthDDL(actual), normalizeHealthDDL(expected))
	}
}

func TestScheduledDailyLocalDSTAndCalendar(t *testing.T) {
	checks := []struct {
		name, after, zone, local, want string
	}{
		{"spring missing minute skipped", "2026-03-07T12:00:00Z", "America/New_York", "02:30", "2026-03-09T06:30:00Z"},
		{"fall fold chooses early instant", "2026-10-31T12:00:00Z", "America/New_York", "01:30", "2026-11-01T05:30:00Z"},
		{"fall late instant does not repeat", "2026-11-01T05:45:00Z", "America/New_York", "01:30", "2026-11-02T06:30:00Z"},
		{"strictly after exact occurrence", "2026-11-01T05:30:00Z", "America/New_York", "01:30", "2026-11-02T06:30:00Z"},
		{"leap day local calendar", "2028-02-28T15:00:00Z", "Asia/Shanghai", "00:30", "2028-02-28T16:30:00Z"},
		{"year boundary local calendar", "2026-12-31T15:00:00Z", "Asia/Shanghai", "00:30", "2026-12-31T16:30:00Z"},
		{"quarter-hour offset", "2026-01-01T00:00:00Z", "Asia/Kathmandu", "08:15", "2026-01-01T02:30:00Z"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			got, err := nextScheduledDailyRun(scheduledInstant(t, check.after), check.zone, check.local)
			if err != nil {
				t.Fatal(err)
			}
			if want := scheduledInstant(t, check.want); !got.Equal(want) {
				t.Fatalf("next = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestScheduledDailyLocalRejectsInvalidInputWithoutHostFallback(t *testing.T) {
	if err := verifyScheduledTZArchive(); err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"", "Local", "/etc/localtime", "../America/New_York", "UTC+05:30", "Not/AZone"} {
		if _, err := loadScheduledTimeZone(zone); !errors.Is(err, errScheduledTimeZoneInvalid) {
			t.Fatalf("zone %q: %v", zone, err)
		}
	}
	for _, local := range []string{"2:30", "24:00", "23:60", "23:59:00", " 02:30", "02.30"} {
		if _, err := nextScheduledDailyRun(time.Now(), "Etc/UTC", local); !errors.Is(err, errScheduledLocalTimeInvalid) {
			t.Fatalf("local time %q: %v", local, err)
		}
	}
}
