package fleet

import "time"

// recurrence mirrors an active row of cars.recurrences with its timezone
// resolved.
type recurrence struct {
	id         string
	firstStart time.Time
	firstEnd   time.Time
	period     string
	location   *time.Location
}

// overlaps ports cars.recurrence_overlaps: estimate which occurrence lands
// near the window, then check it and its neighbors, so cost is constant.
// Occurrence starts keep wall-clock time in the recurrence's timezone across
// DST, and ends sit a fixed absolute duration after their start, both exactly
// like the SQL.
func (r recurrence) overlaps(windowStart, windowEnd time.Time) bool {
	duration := r.firstEnd.Sub(r.firstStart)
	localStart := r.firstStart.In(r.location)
	localWindow := windowStart.In(r.location)

	var guess int
	switch r.period {
	case "weekly":
		guess = int(localWindowDelta(localStart, localWindow) / (7 * 24 * time.Hour))
	case "monthly":
		guess = 12*(localWindow.Year()-localStart.Year()) + int(localWindow.Month()) - int(localStart.Month())
	default: // yearly
		guess = localWindow.Year() - localStart.Year()
	}

	for n := max(guess-1, 0); n <= max(guess+2, 0); n++ {
		var occurrenceStart time.Time
		switch r.period {
		case "weekly":
			occurrenceStart = localStart.AddDate(0, 0, 7*n)
		case "monthly":
			occurrenceStart = addMonthsClamped(localStart, n)
		default:
			occurrenceStart = addMonthsClamped(localStart, 12*n)
		}
		occurrenceEnd := occurrenceStart.Add(duration)
		if occurrenceStart.Before(windowEnd) && windowStart.Before(occurrenceEnd) {
			return true
		}
	}
	return false
}

// localWindowDelta mirrors the SQL's subtraction of wall-clock timestamps:
// the difference between the two local times as if both were UTC.
func localWindowDelta(localStart, localWindow time.Time) time.Duration {
	asUTC := func(local time.Time) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day(),
			local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
	}
	return asUTC(localWindow).Sub(asUTC(localStart))
}

// addMonthsClamped adds months the way Postgres intervals do: Jan 31 + 1
// month is Feb 28, not Mar 3. Go's AddDate would roll the overflow forward.
func addMonthsClamped(local time.Time, months int) time.Time {
	year := local.Year()
	month := int(local.Month()) - 1 + months
	year += month / 12
	month = month % 12
	if month < 0 {
		month += 12
		year--
	}
	day := local.Day()
	if last := daysIn(year, time.Month(month+1)); day > last {
		day = last
	}
	return time.Date(year, time.Month(month+1), day,
		local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), local.Location())
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
