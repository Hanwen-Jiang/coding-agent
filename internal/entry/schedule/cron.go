// Package schedule implements UTC, five-field cron schedules with an hourly floor.
package schedule

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrCron = errors.New("cron must be five numeric UTC fields with a minimum interval of one hour")

type Cron struct {
	minute, hour, day, month, weekday map[int]bool
	dayAny, weekdayAny                bool
}

// Parse accepts standard numeric lists, ranges and steps. Descriptors and local
// time zones are intentionally excluded so a repeated DST hour cannot run twice.
func Parse(expression string) (Cron, error) {
	f := strings.Fields(expression)
	if len(f) != 5 {
		return Cron{}, ErrCron
	}
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	masks := make([]map[int]bool, 5)
	for i, s := range f {
		m, err := parseField(s, limits[i][0], limits[i][1])
		if err != nil {
			return Cron{}, err
		}
		masks[i] = m
	}
	// Two selected minutes necessarily produce a gap below an hour in an active hour.
	if len(masks[0]) != 1 {
		return Cron{}, ErrCron
	}
	if masks[4][7] {
		masks[4][0] = true
		delete(masks[4], 7)
	}
	c := Cron{masks[0], masks[1], masks[2], masks[3], masks[4], strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")}
	if _, ok := c.Latest(time.Date(2032, 12, 31, 23, 59, 0, 0, time.UTC)); !ok {
		return Cron{}, ErrCron
	}
	return c, nil
}

func parseField(s string, low, high int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		base, stepText, stepped := strings.Cut(part, "/")
		step := 1
		if stepped {
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step < 1 || step > high-low+1 {
				return nil, ErrCron
			}
		}
		start, end := low, high
		if base != "*" {
			a, b, ranged := strings.Cut(base, "-")
			var err error
			start, err = strconv.Atoi(a)
			if err != nil {
				return nil, ErrCron
			}
			end = start
			if ranged {
				end, err = strconv.Atoi(b)
				if err != nil {
					return nil, ErrCron
				}
			} else if stepped {
				end = high
			}
		}
		if start < low || end > high || start > end {
			return nil, ErrCron
		}
		for i := start; i <= end; i += step {
			out[i] = true
		}
	}
	if len(out) == 0 {
		return nil, ErrCron
	}
	return out, nil
}

func (c Cron) matchesDay(t time.Time) bool {
	if !c.month[int(t.Month())] {
		return false
	}
	d, w := c.day[t.Day()], c.weekday[int(t.Weekday())]
	if c.dayAny || c.weekdayAny {
		return d && w
	}
	return d || w // Cron's restricted day-of-month/day-of-week union.
}

// Latest returns only the most recent occurrence at or before now. Callers
// persist that timestamp as the trigger ID; they must never replay a backlog.
func (c Cron) Latest(now time.Time) (time.Time, bool) {
	now = now.UTC()
	minute := 0
	for m := range c.minute {
		minute = m
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < 366*8; i++ {
		if c.matchesDay(day) {
			for h := 23; h >= 0; h-- {
				if c.hour[h] {
					t := day.Add(time.Duration(h)*time.Hour + time.Duration(minute)*time.Minute)
					if !t.After(now) {
						return t, true
					}
				}
			}
		}
		day = day.AddDate(0, 0, -1)
	}
	return time.Time{}, false
}

func Validate(expression string) error { _, err := Parse(expression); return err }
func Latest(expression string, now time.Time) (time.Time, error) {
	c, err := Parse(expression)
	if err != nil {
		return time.Time{}, err
	}
	t, ok := c.Latest(now)
	if !ok {
		return time.Time{}, ErrCron
	}
	return t, nil
}
