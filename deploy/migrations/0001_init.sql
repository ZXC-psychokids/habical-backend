CREATE EXTENSION IF NOT EXISTS "pgcrypto";

DO $$ BEGIN
    CREATE TYPE habit_schedule_type AS ENUM ('daily', 'interval', 'weekdays');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE event_schedule_type AS ENUM ('none', 'daily', 'interval', 'weekdays', 'monthly');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE friend_invite_status AS ENUM ('pending', 'accepted', 'rejected');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE feed_item_type AS ENUM ('friend_added', 'habit_streak', 'habit_created', 'shared_habit_reminder');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY,
    email varchar(255) UNIQUE NOT NULL,
    handle varchar(64) UNIQUE NOT NULL,
    password_hash varchar(255) NOT NULL,
    avatar_url text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS user_settings (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    timezone varchar(64) NOT NULL,
    week_starts_on smallint NOT NULL CHECK (week_starts_on IN (1, 7)),
    share_habits boolean NOT NULL,
    share_calendar boolean NOT NULL,
    share_news boolean NOT NULL,
    notify_friend_requests boolean NOT NULL,
    notify_habit_reminders boolean NOT NULL,
    notify_friends_news boolean NOT NULL
);

CREATE TABLE IF NOT EXISTS habits (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title varchar(255) NOT NULL,
    color varchar(32) NOT NULL,
    schedule_type habit_schedule_type NOT NULL,
    interval_days integer NOT NULL CHECK (interval_days >= 1),
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS habit_weekdays (
    habit_id uuid NOT NULL REFERENCES habits(id) ON DELETE CASCADE,
    weekday smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7),
    PRIMARY KEY (habit_id, weekday)
);

CREATE TABLE IF NOT EXISTS shared_habit_pairs (
    id uuid PRIMARY KEY,
    habit1_id uuid UNIQUE NOT NULL REFERENCES habits(id) ON DELETE CASCADE,
    habit2_id uuid UNIQUE NOT NULL REFERENCES habits(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    CHECK (habit1_id <> habit2_id)
);

CREATE TABLE IF NOT EXISTS tasks (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    habit_id uuid NULL REFERENCES habits(id) ON DELETE SET NULL,
    title varchar(255) NOT NULL,
    task_date date NOT NULL,
    manual_color varchar(32) NULL,
    position integer NOT NULL CHECK (position >= 0),
    is_completed boolean NOT NULL
);

CREATE TABLE IF NOT EXISTS event_categories (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title varchar(255) NOT NULL,
    color varchar(32) NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id uuid NOT NULL REFERENCES event_categories(id) ON DELETE RESTRICT,
    task_id uuid NULL UNIQUE REFERENCES tasks(id) ON DELETE SET NULL,
    title varchar(255) NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    schedule_type event_schedule_type NOT NULL,
    interval_days integer NOT NULL CHECK (interval_days >= 1),
    created_at timestamptz NOT NULL,
    CHECK (ends_at > starts_at)
);

CREATE TABLE IF NOT EXISTS event_weekdays (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    weekday smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7),
    PRIMARY KEY (event_id, weekday)
);

CREATE TABLE IF NOT EXISTS friendships (
    id uuid PRIMARY KEY,
    user1_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user2_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    CHECK (user1_id <> user2_id),
    UNIQUE (user1_id, user2_id)
);

CREATE TABLE IF NOT EXISTS friend_invites (
    id uuid PRIMARY KEY,
    sender_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    receiver_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status friend_invite_status NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (sender_user_id <> receiver_user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_friend_invites_pending_pair
ON friend_invites (
    LEAST(sender_user_id, receiver_user_id),
    GREATEST(sender_user_id, receiver_user_id)
)
WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS feed_items (
    id uuid PRIMARY KEY,
    recipient_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type feed_item_type NOT NULL,
    related_user_id uuid NULL REFERENCES users(id) ON DELETE SET NULL,
    related_habit_id uuid NULL REFERENCES habits(id) ON DELETE SET NULL,
    streak_value integer NULL CHECK (streak_value >= 1),
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS shared_habit_reminders (
    id uuid PRIMARY KEY,
    shared_habit_pair_id uuid NOT NULL REFERENCES shared_habit_pairs(id) ON DELETE CASCADE,
    sender_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    CHECK (sender_user_id <> recipient_user_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_shared_habit_reminders_task_day
ON shared_habit_reminders (task_id, date(created_at));

CREATE TABLE IF NOT EXISTS auth_refresh_tokens (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash varchar(255) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL
);

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash varchar(255) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at timestamptz NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS background_jobs (
    id uuid PRIMARY KEY,
    type varchar(64) NOT NULL,
    payload jsonb NOT NULL,
    status varchar(32) NOT NULL,
    run_at timestamptz NOT NULL,
    attempts integer NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_tasks_user_date ON tasks (user_id, task_date);
CREATE INDEX IF NOT EXISTS ix_events_user_starts ON events (user_id, starts_at);
CREATE INDEX IF NOT EXISTS ix_event_categories_user ON event_categories (user_id);
CREATE INDEX IF NOT EXISTS ix_friendships_user1 ON friendships (user1_id);
CREATE INDEX IF NOT EXISTS ix_friendships_user2 ON friendships (user2_id);
CREATE INDEX IF NOT EXISTS ix_friend_invites_receiver_status ON friend_invites (receiver_user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_feed_items_recipient_created ON feed_items (recipient_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_refresh_tokens_user ON auth_refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS ix_password_reset_user ON password_reset_tokens (user_id);
