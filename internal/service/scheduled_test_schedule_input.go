package service

// Independently authored input rules for docs/scheduled-tests-daily-timezone-contract.md.
import (
	"errors"
	"time"
)

type scheduledTestSchedule struct {
	mode     string
	interval int64
	zone     *string
	local    *string
}

var errScheduledScheduleInput = errors.New("invalid scheduled test schedule input")

func scheduledTestCreateKeys(object map[string]any) bool {
	if mode, exists := object["schedule_mode"]; exists {
		if mode == "daily_local" {
			return exactJSONKeys(object, "name", "upstream_id", "scope", "schedule_mode", "time_zone", "local_time", "enabled")
		}
		return exactJSONKeys(object, "name", "upstream_id", "scope", "schedule_mode", "interval_seconds", "enabled")
	}
	return exactJSONKeys(object, "name", "upstream_id", "scope", "interval_seconds", "enabled")
}

func parseScheduledTestCreateSchedule(object map[string]any) (scheduledTestSchedule, error) {
	mode := "interval"
	if value, ok := object["schedule_mode"]; ok {
		parsed, valid := value.(string)
		if !valid {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		mode = parsed
	}
	return parseScheduledTestExplicitSchedule(object, mode)
}

func parseScheduledTestExplicitSchedule(object map[string]any, mode string) (scheduledTestSchedule, error) {
	if mode == "interval" {
		if _, zone := object["time_zone"]; zone {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		if _, local := object["local_time"]; local {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		interval, ok := strictJSONInt64Range(object["interval_seconds"], scheduledTestMinInterval, scheduledTestMaxInterval)
		if !ok {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		return scheduledTestSchedule{mode: mode, interval: interval}, nil
	}
	if mode != "daily_local" {
		return scheduledTestSchedule{}, errScheduledScheduleInput
	}
	if _, interval := object["interval_seconds"]; interval {
		return scheduledTestSchedule{}, errScheduledScheduleInput
	}
	zone, zoneOK := object["time_zone"].(string)
	local, localOK := object["local_time"].(string)
	if !zoneOK || !localOK {
		return scheduledTestSchedule{}, errScheduledScheduleInput
	}
	if _, _, err := parseScheduledLocalTime(local); err != nil {
		return scheduledTestSchedule{}, errScheduledScheduleInput
	}
	if _, err := loadScheduledTimeZone(zone); err != nil {
		return scheduledTestSchedule{}, err
	}
	return scheduledTestSchedule{mode: mode, interval: 86400, zone: &zone, local: &local}, nil
}

func applyScheduledTestPatchSchedule(object map[string]any, current scheduledTestPlan) (scheduledTestSchedule, error) {
	modeValue, hasMode := object["schedule_mode"]
	_, hasInterval := object["interval_seconds"]
	_, hasZone := object["time_zone"]
	_, hasLocal := object["local_time"]
	if hasMode {
		mode, ok := modeValue.(string)
		if !ok {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		return parseScheduledTestExplicitSchedule(object, mode)
	}
	if hasZone || hasLocal || hasInterval && current.ScheduleMode != "interval" {
		return scheduledTestSchedule{}, errScheduledScheduleInput
	}
	result := scheduledTestSchedule{mode: current.ScheduleMode, interval: current.IntervalSeconds, zone: current.TimeZone, local: current.LocalTime}
	if hasInterval {
		interval, ok := strictJSONInt64Range(object["interval_seconds"], scheduledTestMinInterval, scheduledTestMaxInterval)
		if !ok {
			return scheduledTestSchedule{}, errScheduledScheduleInput
		}
		result.interval = interval
	}
	return result, nil
}

func (schedule scheduledTestSchedule) next(after time.Time) (string, error) {
	zone, local := "", ""
	if schedule.zone != nil {
		zone = *schedule.zone
	}
	if schedule.local != nil {
		local = *schedule.local
	}
	next, err := nextScheduledTestRun(after, schedule.mode, schedule.interval, zone, local)
	if err != nil {
		return "", err
	}
	return formatAccountPoolTime(next), nil
}

func scheduledNullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
