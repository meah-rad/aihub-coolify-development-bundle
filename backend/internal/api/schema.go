package api

import (
	"context"
	"database/sql"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
 id BIGSERIAL PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
 role TEXT NOT NULL CHECK (role IN ('admin','developer','viewer')), disabled BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS access_tokens (
 id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), revoked_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS services (
 id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE, owner_id BIGINT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'stopped', url TEXT NOT NULL DEFAULT '',
 container_id TEXT NOT NULL DEFAULT '', network_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE services ADD COLUMN IF NOT EXISTS coolify_application_uuid TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS service_env (
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
 name TEXT NOT NULL, ciphertext BYTEA NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(service_id,name)
);
CREATE TABLE IF NOT EXISTS integration_settings (
 key TEXT PRIMARY KEY, ciphertext BYTEA NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS deployments (
 id TEXT PRIMARY KEY, service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
 sequence BIGINT NOT NULL, status TEXT NOT NULL, commit_sha TEXT NOT NULL DEFAULT '',
 image TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 finished_at TIMESTAMPTZ, idempotency_key TEXT UNIQUE, UNIQUE(service_id, sequence)
);
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS coolify_deployment_uuid TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS jobs (
 id BIGSERIAL PRIMARY KEY, deployment_id TEXT UNIQUE REFERENCES deployments(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'queued', attempts INT NOT NULL DEFAULT 0,
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(), locked_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE jobs ALTER COLUMN deployment_id DROP NOT NULL;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'deploy';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS service_id TEXT REFERENCES services(id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS action TEXT;
CREATE INDEX IF NOT EXISTS jobs_service_status ON jobs(service_id,status);
CREATE TABLE IF NOT EXISTS deployment_events (
 id BIGSERIAL PRIMARY KEY, deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 level TEXT NOT NULL DEFAULT 'info', message TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS audit_events (
 id BIGSERIAL PRIMARY KEY, actor_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
 action TEXT NOT NULL, resource TEXT NOT NULL, detail JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS deployments_service_created ON deployments(service_id, created_at DESC);
CREATE INDEX IF NOT EXISTS events_deployment_id ON deployment_events(deployment_id, id);
CREATE INDEX IF NOT EXISTS jobs_claim ON jobs(status, available_at);
`

func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, schema)
	return err
}

func BootstrapAdmin(ctx context.Context, db *sql.DB, email, password string) error {
	if password == "" {
		return errors.New("ADMIN_PASSWORD must be set to a non-empty value")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role='admin'`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,$2,'admin') ON CONFLICT(email) DO NOTHING`, email, string(hash))
	return err
}
