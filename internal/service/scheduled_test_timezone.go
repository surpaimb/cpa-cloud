package service

// Independently authored from docs/scheduled-tests-daily-timezone-contract.md.
// The pinned IANA archive is loaded directly, never via the host's zoneinfo search path.
import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const scheduledTZArchiveSHA256 = "8F55634D05F8BCA1F7BC7C69C5933428C69357E0BDF565E5BA224E3F88FF12E8"

var (
	errScheduledTimeZoneInvalid     = errors.New("invalid scheduled test time zone")
	errScheduledTimeZoneUnavailable = errors.New("scheduled test time zone data unavailable")
	errScheduledLocalTimeInvalid    = errors.New("invalid scheduled test local time")
)

//go:embed tzdata/zoneinfo.zip
var scheduledTZArchive []byte

var scheduledTZIndex struct {
	once  sync.Once
	files map[string]*zip.File
	err   error
	cache sync.Map // map[string]*time.Location
}

func verifyScheduledTZArchive() error {
	scheduledTZIndex.once.Do(func() {
		sum := sha256.Sum256(scheduledTZArchive)
		if fmt.Sprintf("%X", sum[:]) != scheduledTZArchiveSHA256 {
			scheduledTZIndex.err = errScheduledTimeZoneUnavailable
			return
		}
		archive, err := zip.NewReader(bytes.NewReader(scheduledTZArchive), int64(len(scheduledTZArchive)))
		if err != nil {
			scheduledTZIndex.err = errScheduledTimeZoneUnavailable
			return
		}
		files := make(map[string]*zip.File, len(archive.File))
		for _, file := range archive.File {
			if file.UncompressedSize64 > 1<<20 || file.Name == "" || files[file.Name] != nil {
				scheduledTZIndex.err = errScheduledTimeZoneUnavailable
				return
			}
			files[file.Name] = file
		}
		scheduledTZIndex.files = files
	})
	return scheduledTZIndex.err
}

func loadScheduledTimeZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 128 || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\x00") {
		return nil, errScheduledTimeZoneInvalid
	}
	if err := verifyScheduledTZArchive(); err != nil {
		return nil, err
	}
	if cached, ok := scheduledTZIndex.cache.Load(name); ok {
		return cached.(*time.Location), nil
	}
	file := scheduledTZIndex.files[name]
	if file == nil {
		return nil, errScheduledTimeZoneInvalid
	}
	reader, err := file.Open()
	if err != nil {
		return nil, errScheduledTimeZoneUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, 1<<20+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(data) > 1<<20 {
		return nil, errScheduledTimeZoneUnavailable
	}
	location, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil, errScheduledTimeZoneUnavailable
	}
	actual, _ := scheduledTZIndex.cache.LoadOrStore(name, location)
	return actual.(*time.Location), nil
}

func parseScheduledLocalTime(value string) (hour, minute int, err error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, 0, errScheduledLocalTimeInvalid
	}
	hour = int(value[0]-'0')*10 + int(value[1]-'0')
	minute = int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, 0, errScheduledLocalTimeInvalid
	}
	return hour, minute, nil
}

// scheduledDailyOnDate enumerates actual offset periods surrounding a civil
// minute. A missing minute has no candidate; a repeated minute picks the first
// instant. time.Date(local) cannot define either case deterministically.
func scheduledDailyOnDate(date time.Time, hour, minute int, location *time.Location) (time.Time, bool, error) {
	civil := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.UTC)
	windowEnd := civil.Add(48 * time.Hour)
	cursor := civil.Add(-48 * time.Hour)
	var earliest time.Time
	for steps := 0; cursor.Before(windowEnd); steps++ {
		if steps >= 4096 {
			return time.Time{}, false, errScheduledTimeZoneUnavailable
		}
		zoned := cursor.In(location)
		_, offset := zoned.Zone()
		_, periodEnd := zoned.ZoneBounds()
		candidate := civil.Add(-time.Duration(offset) * time.Second)
		if !candidate.Before(cursor) && candidate.Before(windowEnd) && (periodEnd.IsZero() || candidate.Before(periodEnd)) {
			local := candidate.In(location)
			if local.Year() == date.Year() && local.Month() == date.Month() && local.Day() == date.Day() && local.Hour() == hour && local.Minute() == minute && local.Second() == 0 && (earliest.IsZero() || candidate.Before(earliest)) {
				earliest = candidate
			}
		}
		if periodEnd.IsZero() || !periodEnd.Before(windowEnd) {
			break
		}
		if !periodEnd.After(cursor) {
			return time.Time{}, false, errScheduledTimeZoneUnavailable
		}
		cursor = periodEnd.UTC()
	}
	return earliest, !earliest.IsZero(), nil
}

func nextScheduledDailyRun(after time.Time, zoneName, localTime string) (time.Time, error) {
	hour, minute, err := parseScheduledLocalTime(localTime)
	if err != nil {
		return time.Time{}, err
	}
	location, err := loadScheduledTimeZone(zoneName)
	if err != nil {
		return time.Time{}, err
	}
	local := after.UTC().In(location)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for day := 0; day < 370; day++ {
		candidate, exists, err := scheduledDailyOnDate(date, hour, minute, location)
		if err != nil {
			return time.Time{}, err
		}
		if exists && candidate.After(after) {
			return candidate.UTC(), nil
		}
		date = date.AddDate(0, 0, 1)
	}
	return time.Time{}, errScheduledTimeZoneUnavailable
}

func nextScheduledTestRun(after time.Time, mode string, interval int64, zoneName, localTime string) (time.Time, error) {
	if mode == "interval" {
		if interval < scheduledTestMinInterval || interval > scheduledTestMaxInterval || zoneName != "" || localTime != "" {
			return time.Time{}, errScheduledLocalTimeInvalid
		}
		return after.UTC().Add(time.Duration(interval) * time.Second), nil
	}
	if mode == "daily_local" && interval == 86400 {
		return nextScheduledDailyRun(after, zoneName, localTime)
	}
	return time.Time{}, errScheduledLocalTimeInvalid
}
