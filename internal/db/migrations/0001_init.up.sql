CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  mc_username   TEXT,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'player' CHECK (role IN ('admin', 'player')),
  created_at    INTEGER NOT NULL,
  created_by    TEXT REFERENCES users(id) ON DELETE SET NULL,
  last_login_at INTEGER
);

CREATE TABLE sessions (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  refresh_token_hash TEXT NOT NULL UNIQUE,
  created_at         INTEGER NOT NULL,
  expires_at         INTEGER NOT NULL,
  revoked_at         INTEGER
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE servers (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL UNIQUE,
  directory          TEXT NOT NULL UNIQUE,
  mc_version         TEXT NOT NULL,
  java_version       TEXT NOT NULL,
  port               INTEGER NOT NULL UNIQUE,
  rcon_port          INTEGER NOT NULL UNIQUE,
  rcon_password      TEXT NOT NULL,
  max_memory_mb      INTEGER NOT NULL DEFAULT 2048,
  auto_restart       INTEGER NOT NULL DEFAULT 1,
  created_at         INTEGER NOT NULL,
  status             TEXT NOT NULL DEFAULT 'stopped',
  connection_address TEXT
);

CREATE TABLE invites (
  id                  TEXT PRIMARY KEY,
  token_hash          TEXT NOT NULL UNIQUE,
  created_by          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at          INTEGER NOT NULL,
  expires_at          INTEGER NOT NULL,
  used_at             INTEGER,
  used_by             TEXT REFERENCES users(id) ON DELETE SET NULL,
  whitelist_server_id TEXT REFERENCES servers(id) ON DELETE SET NULL
);

CREATE TABLE server_events (
  id         TEXT PRIMARY KEY,
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  detail     TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_events_server ON server_events(server_id, created_at DESC);

CREATE TABLE backups (
  id         TEXT PRIMARY KEY,
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  filename   TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  tag        TEXT,
  created_at INTEGER NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_backups_server ON backups(server_id, created_at DESC);

CREATE TABLE plugins (
  id           TEXT PRIMARY KEY,
  server_id    TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  version      TEXT NOT NULL,
  source       TEXT NOT NULL,
  source_id    TEXT,
  filename     TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  installed_at INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE(server_id, filename)
);

CREATE TABLE world_imports (
  id                   TEXT PRIMARY KEY,
  server_id            TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  imported_at          INTEGER NOT NULL,
  imported_by          TEXT NOT NULL REFERENCES users(id),
  source_name          TEXT NOT NULL,
  source_mc_version    TEXT NOT NULL,
  pre_import_backup_id TEXT REFERENCES backups(id) ON DELETE SET NULL
);

CREATE TABLE whitelist_queue (
  id          TEXT PRIMARY KEY,
  server_id   TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  mc_username TEXT NOT NULL,
  queued_at   INTEGER NOT NULL,
  applied_at  INTEGER
);

CREATE TABLE webhooks (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  url_enc     TEXT NOT NULL,
  events_json TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);
