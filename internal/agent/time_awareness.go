package agent

import (
	"fmt"
	"strings"
	"time"
)

// TimeAwareness provides temporal context for the agent: current wall clock time,
// session start time, and elapsed duration. It injects a compact time-context
// block into system prompts so the LLM always knows "what time it is".
type TimeAwareness struct {
	timezone     *time.Location
	sessionStart time.Time
	enabled      bool
}

// NewTimeAwareness creates a TimeAwareness instance.
// If timezone is empty or invalid, UTC is used.
// When enabled is false, BuildContextBlock returns an empty string (no-op).
func NewTimeAwareness(timezone string, enabled bool) *TimeAwareness {
	loc := time.UTC
	if timezone != "" {
		if l, err := time.LoadLocation(timezone); err == nil {
			loc = l
		}
	}
	return &TimeAwareness{
		timezone:     loc,
		sessionStart: time.Now(),
		enabled:      enabled,
	}
}

// Now returns the current time in the configured timezone.
func (ta *TimeAwareness) Now() time.Time {
	return time.Now().In(ta.timezone)
}

// SessionElapsed returns how long has elapsed since the TimeAwareness was created
// (i.e. since the server started or was last reconfigured).
func (ta *TimeAwareness) SessionElapsed() time.Duration {
	return time.Since(ta.sessionStart)
}

// BuildContextBlock returns a brief XML-tagged block with temporal context
// suitable for prepending to a system prompt.  Returns empty string when disabled.
func (ta *TimeAwareness) BuildContextBlock() string {
	if !ta.enabled {
		return ""
	}

	now := ta.Now()
	elapsed := ta.SessionElapsed()

	var sb strings.Builder
	sb.WriteString("<time_context>\n")
	sb.WriteString(fmt.Sprintf("  Current date and time : %s\n", now.Format("2006-01-02 15:04:05 MST")))
	sb.WriteString(fmt.Sprintf("  Day of week           : %s\n", now.Weekday().String()))
	sb.WriteString(fmt.Sprintf("  Unix timestamp        : %d\n", now.Unix()))
	sb.WriteString(fmt.Sprintf("  Session age           : %s\n", formatDuration(elapsed)))
	sb.WriteString("</time_context>\n")
	return sb.String()
}

// FormatCurrentTime returns a human-readable string of the current time for
// use in tool responses.
func (ta *TimeAwareness) FormatCurrentTime() string {
	now := ta.Now()
	return fmt.Sprintf(
		"Current time: %s\nTimezone: %s\nUnix timestamp: %d\nSession uptime: %s",
		now.Format("2006-01-02 15:04:05 MST"),
		ta.timezone.String(),
		now.Unix(),
		formatDuration(ta.SessionElapsed()),
	)
}

// formatDuration converts a duration into a human-readable string like "2h 15m 30s".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
