package utils

import (
	"strings"
	"sync"
	"time"
)

var CNLoc = time.FixedZone("UTC", 8*60*60)

// SanitizeTimeString normalizes special/Unicode whitespace characters that some
// network disk APIs (e.g. Quark/夸克网盘) embed inside timestamp strings — such as
// U+202F (NARROW NO-BREAK SPACE) or U+00A0 (NO-BREAK SPACE) — into a regular ASCII
// space, so that time.Parse / time.ParseInLocation can parse the value. It also
// collapses consecutive spaces and trims the result.
//
// Background: Quark returns modified-time strings containing U+202F, e.g.
// "Aug 12, 2026, 1:57:54\u202fPM +08". Go's time.Parse matches the space in its
// layout against a literal 0x20 byte and rejects U+202F, which aborts the whole
// cross-storage copy task. Sanitizing the string first avoids that.
func SanitizeTimeString(s string) string {
	if s == "" {
		return s
	}
	replace := func(r rune) rune {
		switch r {
		case '\u00A0', '\u00AD', '\u0085', '\u1680', '\u2000', '\u2001', '\u2002',
			'\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009',
			'\u200A', '\u200B', '\u2028', '\u2029', '\u202F', '\u205F', '\u2060', '\u3000', '\uFEFF':
			return ' '
		default:
			return r
		}
	}
	s = strings.Map(replace, s)
	return strings.Join(strings.Fields(s), " ")
}

// ParseTime is a drop-in replacement for time.Parse that first sanitizes the
// value string (see SanitizeTimeString) so timestamps from misbehaving APIs
// (e.g. Quark) parse correctly.
func ParseTime(layout, value string) (time.Time, error) {
	return time.Parse(layout, SanitizeTimeString(value))
}

// ParseTimeInLocation is a drop-in replacement for time.ParseInLocation that
// first sanitizes the value string.
func ParseTimeInLocation(layout, value string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(layout, SanitizeTimeString(value), loc)
}

func MustParseCNTime(str string) time.Time {
	lastOpTime, _ := time.ParseInLocation("2006-01-02 15:04:05 -07", SanitizeTimeString(str)+" +08", CNLoc)
	return lastOpTime
}

func NewDebounce(interval time.Duration) func(f func()) {
	var timer *time.Timer
	var lock sync.Mutex
	return func(f func()) {
		lock.Lock()
		defer lock.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(interval, f)
	}
}

func NewDebounce2(interval time.Duration, f func()) func() {
	var timer *time.Timer
	var lock sync.Mutex
	return func() {
		lock.Lock()
		defer lock.Unlock()
		if timer == nil {
			timer = time.AfterFunc(interval, f)
		}
		timer.Reset(interval)
	}
}

func NewThrottle(interval time.Duration) func(func()) {
	var lastCall time.Time
	var lock sync.Mutex
	return func(fn func()) {
		lock.Lock()
		defer lock.Unlock()

		now := time.Now()
		if now.Sub(lastCall) >= interval {
			lastCall = now
			go fn()
		}
	}
}

func NewThrottle2(interval time.Duration, fn func()) func() {
	var lastCall time.Time
	var lock sync.Mutex
	return func() {
		lock.Lock()
		defer lock.Unlock()

		now := time.Now()
		if now.Sub(lastCall) >= interval {
			lastCall = now
			go fn()
		}
	}
}
