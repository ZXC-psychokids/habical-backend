package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

type Job struct {
	ID       string
	Type     string
	Payload  json.RawMessage
	RunAt    time.Time
	Attempts int
}

type Habit struct {
	ID           string
	UserID       string
	Title        string
	Color        string
	ScheduleType string
	IntervalDays int
	Weekdays     []int
	CreatedAt    time.Time
}

type Task struct {
	ID       string
	UserID   string
	HabitID  string
	Title    string
	TaskDate time.Time
	Position int
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) ReservePendingJobs(ctx context.Context, limit int) ([]Job, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
        WITH reserved AS (
            SELECT id
            FROM background_jobs
            WHERE status = 'pending' AND run_at <= now()
            ORDER BY run_at ASC
            FOR UPDATE SKIP LOCKED
            LIMIT $1
        )
        UPDATE background_jobs
        SET status = 'processing', updated_at = now()
        FROM reserved
        WHERE background_jobs.id = reserved.id
        RETURNING background_jobs.id, background_jobs.type, background_jobs.payload, background_jobs.run_at, background_jobs.attempts
    `, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := make([]Job, 0)
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.Type, &job.Payload, &job.RunAt, &job.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *Repository) MarkJobCompleted(ctx context.Context, jobID string) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE background_jobs
        SET status = 'completed', attempts = attempts + 1, updated_at = now()
        WHERE id = $1
    `, jobID)
	return err
}

func (r *Repository) MarkJobFailed(ctx context.Context, jobID string, attempts int, retryDelay time.Duration, maxAttempts int) error {
	status := "pending"
	nextRun := time.Now().UTC().Add(retryDelay)
	if attempts+1 >= maxAttempts {
		status = "failed"
		nextRun = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
        UPDATE background_jobs
        SET status = $1, attempts = attempts + 1, run_at = $2, updated_at = now()
        WHERE id = $3
    `, status, nextRun, jobID)
	return err
}

func (r *Repository) CreateBackgroundJob(ctx context.Context, id, jobType string, payload any, runAt time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
        INSERT INTO background_jobs (id, type, payload, status, run_at, attempts, created_at, updated_at)
        VALUES ($1, $2, $3, 'pending', $4, 0, now(), now())
    `, id, jobType, raw, runAt)
	return err
}

func (r *Repository) GetHabit(ctx context.Context, habitID string) (Habit, error) {
	var habit Habit
	err := r.pool.QueryRow(ctx, `
        SELECT id, user_id, title, color, schedule_type, interval_days, created_at
        FROM habits
        WHERE id = $1
    `, habitID).Scan(&habit.ID, &habit.UserID, &habit.Title, &habit.Color, &habit.ScheduleType, &habit.IntervalDays, &habit.CreatedAt)
	if err != nil {
		return habit, err
	}

	if habit.ScheduleType == "weekdays" {
		weekdays, err := r.getHabitWeekdays(ctx, habitID)
		if err != nil {
			return habit, err
		}
		habit.Weekdays = weekdays
	}
	return habit, nil
}

func (r *Repository) getHabitWeekdays(ctx context.Context, habitID string) ([]int, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT weekday FROM habit_weekdays WHERE habit_id = $1 ORDER BY weekday
    `, habitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	weekdays := make([]int, 0)
	for rows.Next() {
		var weekday int
		if err := rows.Scan(&weekday); err != nil {
			return nil, err
		}
		weekdays = append(weekdays, weekday)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return weekdays, nil
}

func (r *Repository) DeleteFutureIncompleteHabitTasks(ctx context.Context, habitID string, fromDate time.Time) error {
	_, err := r.pool.Exec(ctx, `
        DELETE FROM tasks
        WHERE habit_id = $1 AND task_date >= $2 AND is_completed = FALSE
    `, habitID, fromDate)
	return err
}

func (r *Repository) TaskExists(ctx context.Context, habitID string, taskDate time.Time) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM tasks WHERE habit_id = $1 AND task_date = $2
        )
    `, habitID, taskDate).Scan(&exists)
	return exists, err
}

func (r *Repository) NextPosition(ctx context.Context, userID string, taskDate time.Time) (int, error) {
	var maxPosition int
	err := r.pool.QueryRow(ctx, `
        SELECT COALESCE(MAX(position), -1) FROM tasks
        WHERE user_id = $1 AND task_date = $2
    `, userID, taskDate).Scan(&maxPosition)
	if err != nil {
		return 0, err
	}
	return maxPosition + 1, nil
}

func (r *Repository) CreateTask(ctx context.Context, task Task) error {
	_, err := r.pool.Exec(ctx, `
        INSERT INTO tasks (id, user_id, habit_id, title, task_date, position, is_completed)
        VALUES ($1, $2, $3, $4, $5, $6, FALSE)
    `, task.ID, task.UserID, task.HabitID, task.Title, task.TaskDate, task.Position)
	return err
}
