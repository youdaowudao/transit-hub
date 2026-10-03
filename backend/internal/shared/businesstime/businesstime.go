package businesstime

import "time"

const (
	Timezone   = "Asia/Singapore"
	dateLayout = "2006-01-02"
)

var singaporeLocation = loadLocation()

func loadLocation() *time.Location {
	location, err := time.LoadLocation(Timezone)
	if err == nil {
		return location
	}
	return time.FixedZone(Timezone, 8*60*60)
}

func Location() *time.Location {
	return singaporeLocation
}

func Today() string {
	return DateAt(time.Now())
}

func DateAt(value time.Time) string {
	return value.In(singaporeLocation).Format(dateLayout)
}

func Bounds(date string) (time.Time, time.Time, error) {
	day, err := time.ParseInLocation(dateLayout, date, singaporeLocation)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, singaporeLocation)
	end := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, int(time.Second-time.Nanosecond), singaporeLocation)
	return start, end, nil
}
