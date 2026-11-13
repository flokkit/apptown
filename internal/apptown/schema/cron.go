package schema

import (
	"fmt"
	"github.com/robfig/cron/v3"
	"strconv"
	"strings"
	"time"
)

// ParseCron accepts the five-field schedules supported by workflow declarations.
// Editor validation and scheduling use exactly the same grammar.
func ParseCron(expr string) (cron.Schedule, error) {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expr)
}

// CronWords describes common five-field declarations in UTC.
func CronWords(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) == 5 {
		minute, em := strconv.Atoi(fields[0])
		hour, eh := strconv.Atoi(fields[1])
		if em == nil && eh == nil && minute >= 0 && minute < 60 && hour >= 0 && hour < 24 && fields[3] == "*" {
			at := fmt.Sprintf(" at %02d:%02d UTC", hour, minute)
			if fields[2] == "*" {
				if fields[4] == "*" {
					return "every day" + at
				}
				if day, err := strconv.Atoi(fields[4]); err == nil && day >= 0 && day <= 6 {
					return "every " + time.Weekday(day).String() + at
				}
			}
			if day, err := strconv.Atoi(fields[2]); err == nil && day >= 1 && day <= 31 && fields[4] == "*" {
				return fmt.Sprintf("on day %d of each month%s", day, at)
			}
		}
	}
	return "on cron `" + expr + "` (UTC)"
}
