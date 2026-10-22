package schema

import "github.com/robfig/cron/v3"

// ParseCron accepts the five-field schedules supported by workflow declarations.
// Editor validation and scheduling use exactly the same grammar.
func ParseCron(expr string) (cron.Schedule, error) {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expr)
}
