package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"habical/backend/libs/idgen"
	"habical/backend/services/worker/internal/config"
	"habical/backend/services/worker/internal/repository"
)

type App struct {
	repo *repository.Repository
	cfg  config.Config
	log  *slog.Logger
}

type habitJobPayload struct {
	HabitID   string `json:"habitId"`
	DaysAhead int    `json:"daysAhead,omitempty"`
}

type streakJobPayload struct {
	HabitID string `json:"habitId"`
	UserID  string `json:"userId,omitempty"`
}

func New(repo *repository.Repository, cfg config.Config, log *slog.Logger) *App {
	return &App{repo: repo, cfg: cfg, log: log}
}

func (a *App) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := a.processDueJobs(ctx); err != nil {
			a.log.Error("worker_process_due_jobs_failed", "error", err.Error())
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *App) processDueJobs(ctx context.Context) error {
	jobs, err := a.repo.ReservePendingJobs(ctx, a.cfg.BatchSize)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	for _, job := range jobs {
		a.log.Info("job_started", "job_id", job.ID, "job_type", job.Type)
		if err := a.processJob(ctx, job); err != nil {
			if err2 := a.repo.MarkJobFailed(ctx, job.ID, job.Attempts, a.cfg.RetryDelay, a.cfg.MaxAttempts); err2 != nil {
				a.log.Error("worker_mark_job_failed_failed", "job_id", job.ID, "job_type", job.Type, "error", err2.Error())
			}
			a.log.Error("job_failed", "job_id", job.ID, "job_type", job.Type, "error", err.Error())
			continue
		}
		if err := a.repo.MarkJobCompleted(ctx, job.ID); err != nil {
			a.log.Error("worker_mark_job_completed_failed", "job_id", job.ID, "job_type", job.Type, "error", err.Error())
			continue
		}
		a.log.Info("job_completed", "job_id", job.ID, "job_type", job.Type)
	}
	return nil
}

func (a *App) processJob(ctx context.Context, job repository.Job) error {
	switch job.Type {
	case "generate_future_tasks":
		return a.handleHabitTaskJob(ctx, job, false)
	case "rebuild_future_tasks":
		return a.handleHabitTaskJob(ctx, job, true)
	case "compute_habit_streaks":
		return a.handleComputeHabitStreaks(ctx, job)
	default:
		return fmt.Errorf("unknown job type: %s", job.Type)
	}
}

func (a *App) handleHabitTaskJob(ctx context.Context, job repository.Job, rebuild bool) error {
	var payload habitJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	if payload.HabitID == "" {
		return fmt.Errorf("habitId is required")
	}
	habit, err := a.repo.GetHabit(ctx, payload.HabitID)
	if err != nil {
		return err
	}
	if payload.DaysAhead <= 0 {
		payload.DaysAhead = 14
	}
	startDate := time.Now().UTC().Truncate(24 * time.Hour)
	endDate := startDate.AddDate(0, 0, payload.DaysAhead)

	if rebuild {
		if err := a.repo.DeleteFutureIncompleteHabitTasks(ctx, habit.ID, startDate); err != nil {
			return err
		}
	}

	return a.generateTasksForHabit(ctx, habit, startDate, endDate)
}

func (a *App) handleComputeHabitStreaks(ctx context.Context, job repository.Job) error {
	var payload streakJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	if payload.HabitID == "" {
		return fmt.Errorf("habitId is required")
	}
	_, err := a.repo.GetHabit(ctx, payload.HabitID)
	return err
}

func (a *App) generateTasksForHabit(ctx context.Context, habit repository.Habit, from, to time.Time) error {
	for current := from; !current.After(to); current = current.AddDate(0, 0, 1) {
		if !a.isHabitScheduledForDate(habit, current) {
			continue
		}
		exists, err := a.repo.TaskExists(ctx, habit.ID, current)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		position, err := a.repo.NextPosition(ctx, habit.UserID, current)
		if err != nil {
			return err
		}
		task := repository.Task{
			ID:       idgen.New(),
			UserID:   habit.UserID,
			HabitID:  habit.ID,
			Title:    habit.Title,
			TaskDate: current,
			Position: position,
		}
		if err := a.repo.CreateTask(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) isHabitScheduledForDate(habit repository.Habit, date time.Time) bool {
	switch habit.ScheduleType {
	case "daily":
		return true
	case "interval":
		startDate := habit.CreatedAt.UTC().Truncate(24 * time.Hour)
		targetDate := date.UTC().Truncate(24 * time.Hour)
		if targetDate.Before(startDate) {
			return false
		}
		days := int(targetDate.Sub(startDate).Hours() / 24)
		return habit.IntervalDays > 0 && days%habit.IntervalDays == 0
	case "weekdays":
		weekday := normalizeWeekday(date.Weekday())
		for _, value := range habit.Weekdays {
			if value == weekday {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func normalizeWeekday(day time.Weekday) int {
	if day == time.Sunday {
		return 7
	}
	return int(day)
}
