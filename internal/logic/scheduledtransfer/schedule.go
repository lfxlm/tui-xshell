// Package scheduledtransfer contains schedule parsing and transfer policy
// shared by the SQLite repository, TUI, and background runner.
package scheduledtransfer

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

const (
	MaxCronSpecLength = 128
	MinEveryInterval  = time.Minute
)

// ValidateSchedule accepts standard five-field cron expressions, cron
// descriptors, and @every intervals of at least one minute.
func ValidateSchedule(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return errors.New("计划表达式不能为空")
	}
	if len([]rune(spec)) > MaxCronSpecLength {
		return fmt.Errorf("计划表达式不能超过 %d 个字符", MaxCronSpecLength)
	}
	if strings.HasPrefix(spec, "@every") {
		fields := strings.Fields(spec)
		if len(fields) != 2 || fields[0] != "@every" {
			return errors.New("@every 格式应为 @every 1h30m")
		}
		interval, err := time.ParseDuration(fields[1])
		if err != nil {
			return fmt.Errorf("解析 @every 间隔：%w", err)
		}
		if interval < MinEveryInterval {
			return fmt.Errorf("@every 间隔不能短于 %s", MinEveryInterval)
		}
		return nil
	}
	if _, err := cron.ParseStandard(spec); err != nil {
		return fmt.Errorf("无效的 Cron 计划：%w", err)
	}
	return nil
}

// LoadLocation resolves an IANA timezone. Empty and "Local" mean the
// scheduler machine's local timezone.
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "Local") {
		return time.Local, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("无效的时区 %q：%w", name, err)
	}
	return location, nil
}

// ValidateTimeZone verifies that a schedule's timezone can be loaded.
func ValidateTimeZone(name string) error {
	_, err := LoadLocation(name)
	return err
}

// NextRun returns the next scheduled instant after after.
func NextRun(spec string, after time.Time) (time.Time, error) {
	return NextRunInLocation(spec, "Local", after)
}

// NextRunInLocation returns the next scheduled instant in the requested
// timezone. @every remains an elapsed duration, independent of timezone.
func NextRunInLocation(spec, timeZone string, after time.Time) (time.Time, error) {
	if err := ValidateSchedule(spec); err != nil {
		return time.Time{}, err
	}
	location, err := LoadLocation(timeZone)
	if err != nil {
		return time.Time{}, err
	}
	cronSpec := strings.TrimSpace(spec)
	if !strings.HasPrefix(cronSpec, "@every") {
		cronSpec = "CRON_TZ=" + location.String() + " " + cronSpec
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(cronSpec)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析 Cron 计划：%w", err)
	}
	next := schedule.Next(after.In(location))
	if next.IsZero() {
		return time.Time{}, errors.New("计划表达式没有下次执行时间")
	}
	return next, nil
}
