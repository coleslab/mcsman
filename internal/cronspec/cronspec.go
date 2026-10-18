// Package cronspec parses mcsman schedule strings: a Go duration ("1h",
// "30m", also "1d") meaning "every", or a standard 5-field cron expression
// ("0 4 * * *"), or "off".
package cronspec

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"mcsman/internal/config"
)

const Off = "off"

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Parse returns the schedule for spec, or nil for "off".
func Parse(spec string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, Off) {
		return nil, nil
	}
	if d, err := config.ParseDuration(spec); err == nil {
		if d < 5*time.Minute {
			return nil, fmt.Errorf("interval %s is too short (minimum 5m)", d)
		}
		return cron.Every(d), nil
	}
	s, err := parser.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("%q is not a duration (like 6h) or cron expression (like \"0 4 * * *\")", spec)
	}
	return s, nil
}

// Validate checks spec without returning the schedule.
func Validate(spec string) error {
	_, err := Parse(spec)
	return err
}
