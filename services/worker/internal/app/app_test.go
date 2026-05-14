package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"habical/backend/libs/logger"
	"habical/backend/services/worker/internal/config"
	"habical/backend/services/worker/internal/repository"
)

type fakeRepo struct {
	jobs             []repository.Job
	reserveErr       error
	markCompletedIDs []string
	markFailedCalls  []struct {
		jobID       string
		attempts    int
		retryDelay  time.Duration
		maxAttempts int
	}
	habitByID   map[string]repository.Habit
	getHabitErr error
}

func (f *fakeRepo) ReservePendingJobs(_ context.Context, limit int) ([]repository.Job, error) {
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	if limit >= len(f.jobs) {
		return append([]repository.Job(nil), f.jobs...), nil
	}
	return append([]repository.Job(nil), f.jobs[:limit]...), nil
}
func (f *fakeRepo) MarkJobCompleted(_ context.Context, jobID string) error {
	f.markCompletedIDs = append(f.markCompletedIDs, jobID)
	return nil
}
func (f *fakeRepo) MarkJobFailed(_ context.Context, jobID string, attempts int, retryDelay time.Duration, maxAttempts int) error {
	f.markFailedCalls = append(f.markFailedCalls, struct {
		jobID       string
		attempts    int
		retryDelay  time.Duration
		maxAttempts int
	}{jobID: jobID, attempts: attempts, retryDelay: retryDelay, maxAttempts: maxAttempts})
	return nil
}
func (f *fakeRepo) GetHabit(_ context.Context, habitID string) (repository.Habit, error) {
	if f.getHabitErr != nil {
		return repository.Habit{}, f.getHabitErr
	}
	habit, ok := f.habitByID[habitID]
	if !ok {
		return repository.Habit{}, errors.New("not found")
	}
	return habit, nil
}
func (f *fakeRepo) DeleteFutureIncompleteHabitTasks(context.Context, string, time.Time) error {
	return nil
}
func (f *fakeRepo) TaskExists(context.Context, string, time.Time) (bool, error)  { return true, nil }
func (f *fakeRepo) NextPosition(context.Context, string, time.Time) (int, error) { return 0, nil }
func (f *fakeRepo) CreateTask(context.Context, repository.Task) error            { return nil }

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

func TestProcessDueJobsCompletesSuccessfulJob(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"habitId": "h1", "daysAhead": 1})
	repo := &fakeRepo{
		jobs: []repository.Job{{ID: "job-1", Type: "generate_future_tasks", Payload: payload, Attempts: 0}},
		habitByID: map[string]repository.Habit{
			"h1": {ID: "h1", UserID: "u1", Title: "t", ScheduleType: "daily", CreatedAt: time.Now().UTC()},
		},
	}
	app := New(repo, config.Config{BatchSize: 10, RetryDelay: time.Second, MaxAttempts: 3}, logger.New("worker-test"))
	if err := app.processDueJobs(context.Background()); err != nil {
		t.Fatalf("processDueJobs error: %v", err)
	}
	if len(repo.markCompletedIDs) != 1 || repo.markCompletedIDs[0] != "job-1" {
		t.Fatalf("expected completed job-1, got %+v", repo.markCompletedIDs)
	}
	if len(repo.markFailedCalls) != 0 {
		t.Fatalf("expected no failed calls, got %+v", repo.markFailedCalls)
	}
}

func TestProcessDueJobsMarksFailedOnUnknownJobType(t *testing.T) {
	repo := &fakeRepo{
		jobs: []repository.Job{{ID: "job-1", Type: "unknown_type", Attempts: 2}},
	}
	cfg := config.Config{BatchSize: 10, RetryDelay: 2 * time.Second, MaxAttempts: 3}
	app := New(repo, cfg, logger.New("worker-test"))
	if err := app.processDueJobs(context.Background()); err != nil {
		t.Fatalf("processDueJobs error: %v", err)
	}
	if len(repo.markCompletedIDs) != 0 {
		t.Fatalf("expected no completed calls, got %+v", repo.markCompletedIDs)
	}
	if len(repo.markFailedCalls) != 1 {
		t.Fatalf("expected 1 failed call, got %+v", repo.markFailedCalls)
	}
	call := repo.markFailedCalls[0]
	if call.jobID != "job-1" || call.attempts != 2 || call.maxAttempts != 3 {
		t.Fatalf("unexpected failed call: %+v", call)
	}
}

func TestProcessDueJobsPassesRetryParamsWhenAttemptsBelowMax(t *testing.T) {
	repo := &fakeRepo{
		jobs: []repository.Job{{ID: "job-retry", Type: "unknown_type", Attempts: 1}},
	}
	cfg := config.Config{BatchSize: 10, RetryDelay: 5 * time.Second, MaxAttempts: 3}
	app := New(repo, cfg, logger.New("worker-test"))
	if err := app.processDueJobs(context.Background()); err != nil {
		t.Fatalf("processDueJobs error: %v", err)
	}
	if len(repo.markFailedCalls) != 1 {
		t.Fatalf("expected 1 failed call, got %+v", repo.markFailedCalls)
	}
	call := repo.markFailedCalls[0]
	if call.jobID != "job-retry" || call.attempts != 1 || call.maxAttempts != 3 || call.retryDelay != 5*time.Second {
		t.Fatalf("unexpected failed call: %+v", call)
	}
}

func TestProcessDueJobsPassesFailedParamsWhenAttemptsAtMaxBoundary(t *testing.T) {
	repo := &fakeRepo{
		jobs: []repository.Job{{ID: "job-failed", Type: "unknown_type", Attempts: 2}},
	}
	cfg := config.Config{BatchSize: 10, RetryDelay: 7 * time.Second, MaxAttempts: 3}
	app := New(repo, cfg, logger.New("worker-test"))
	if err := app.processDueJobs(context.Background()); err != nil {
		t.Fatalf("processDueJobs error: %v", err)
	}
	if len(repo.markFailedCalls) != 1 {
		t.Fatalf("expected 1 failed call, got %+v", repo.markFailedCalls)
	}
	call := repo.markFailedCalls[0]
	if call.jobID != "job-failed" || call.attempts != 2 || call.maxAttempts != 3 || call.retryDelay != 7*time.Second {
		t.Fatalf("unexpected failed call: %+v", call)
	}
}

func TestProcessDueJobsRespectsBatchSize(t *testing.T) {
	repo := &fakeRepo{
		jobs: []repository.Job{
			{ID: "job-1", Type: "unknown_type", Attempts: 0},
			{ID: "job-2", Type: "unknown_type", Attempts: 0},
		},
	}
	app := New(repo, config.Config{BatchSize: 1, RetryDelay: time.Second, MaxAttempts: 3}, logger.New("worker-test"))
	if err := app.processDueJobs(context.Background()); err != nil {
		t.Fatalf("processDueJobs error: %v", err)
	}
	if len(repo.markFailedCalls) != 1 || repo.markFailedCalls[0].jobID != "job-1" {
		t.Fatalf("expected only first job processed, got %+v", repo.markFailedCalls)
	}
}

func TestProcessDueJobsReturnsReserveError(t *testing.T) {
	repo := &fakeRepo{reserveErr: errors.New("reserve failed")}
	app := New(repo, config.Config{BatchSize: 10}, logger.New("worker-test"))
	err := app.processDueJobs(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
