package app

import (
	"testing"
	"time"

	"habical/backend/libs/logger"
	"habical/backend/services/worker/internal/config"
	"habical/backend/services/worker/internal/repository"
)

func TestIsHabitScheduledForDate(t *testing.T) {
	createdAt := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		habit repository.Habit
		date  time.Time
		want  bool
	}{
		{
			name:  "daily always scheduled",
			habit: repository.Habit{ScheduleType: "daily", CreatedAt: createdAt},
			date:  time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
			want:  true,
		},
		{
			name:  "interval every second day",
			habit: repository.Habit{ScheduleType: "interval", IntervalDays: 2, CreatedAt: createdAt},
			date:  time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC),
			want:  true,
		},
		{
			name:  "interval non-scheduled day",
			habit: repository.Habit{ScheduleType: "interval", IntervalDays: 2, CreatedAt: createdAt},
			date:  time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC),
			want:  false,
		},
		{
			name:  "weekdays monday",
			habit: repository.Habit{ScheduleType: "weekdays", Weekdays: []int{1, 3, 5}, CreatedAt: createdAt},
			date:  time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
			want:  true,
		},
		{
			name:  "weekdays sunday",
			habit: repository.Habit{ScheduleType: "weekdays", Weekdays: []int{7}, CreatedAt: createdAt},
			date:  time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
			want:  true,
		},
		{
			name:  "interval before creation",
			habit: repository.Habit{ScheduleType: "interval", IntervalDays: 3, CreatedAt: createdAt},
			date:  time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC),
			want:  false,
		},
	}

	app := New(nil, config.Config{}, logger.New("worker-test"))
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := app.isHabitScheduledForDate(tc.habit, tc.date)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
