package lbug

// #include "lbug.h"
// #include <stdlib.h>
import "C"

import (
	"math"
	"time"
)

// unixEpoch returns the Unix epoch time.
func unixEpoch() time.Time {
	return time.Unix(0, 0)
}

// timeToLbugDate converts a time.Time to a lbug_date_t.
func timeToLbugDate(inputTime time.Time) C.lbug_date_t {
	diff := inputTime.Sub(unixEpoch())
	diffDays := math.Floor(diff.Hours() / 24)
	cLbugDate := C.lbug_date_t{}
	cLbugDate.days = C.int32_t(diffDays)
	return cLbugDate
}

// lbugDateToTime converts a lbug_date_t to a time.Time in UTC.
func lbugDateToTime(cLbugDate C.lbug_date_t) time.Time {
	diff := time.Duration(cLbugDate.days) * 24 * time.Hour
	return unixEpoch().UTC().Add(diff)
}

// timeToLbugTimestamp converts a time.Time to a lbug_timestamp_t.
func timeToLbugTimestamp(inputTime time.Time) C.lbug_timestamp_t {
	nanoseconds := inputTime.UnixNano()
	microseconds := nanoseconds / 1000
	cLbugTime := C.lbug_timestamp_t{}
	cLbugTime.value = C.int64_t(microseconds)
	return cLbugTime
}

// timeToLbugTimestampNs converts a time.Time to a lbug_timestamp_ns_t.
func timeToLbugTimestampNs(inputTime time.Time) C.lbug_timestamp_ns_t {
	nanoseconds := inputTime.UnixNano()
	cLbugTime := C.lbug_timestamp_ns_t{}
	cLbugTime.value = C.int64_t(nanoseconds)
	return cLbugTime
}

// timeHasNanoseconds returns true if the time.Time has non-zero nanoseconds.
func timeHasNanoseconds(inputTime time.Time) bool {
	return inputTime.Nanosecond() != 0
}

// durationToLbugInterval converts a time.Duration to a lbug_interval_t.
func durationToLbugInterval(inputDuration time.Duration) C.lbug_interval_t {
	microseconds := inputDuration.Microseconds()

	cLbugInterval := C.lbug_interval_t{}
	cLbugInterval.micros = C.int64_t(microseconds)
	return cLbugInterval
}

// lbugIntervalToDuration converts a lbug_interval_t to a time.Duration.
func lbugIntervalToDuration(cLbugInterval C.lbug_interval_t) time.Duration {
	days := cLbugInterval.days
	months := cLbugInterval.months
	microseconds := cLbugInterval.micros
	totalDays := int64(days) + int64(months)*30
	totalSeconds := totalDays * 24 * 60 * 60
	totalMicroseconds := totalSeconds*1000000 + int64(microseconds)
	totalNanoseconds := totalMicroseconds * 1000
	return time.Duration(totalNanoseconds)
}
