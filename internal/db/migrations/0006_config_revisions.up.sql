-- Previous contents of config files edited through mcsman, newest last.
-- server.properties revisions have the RCON password masked.
CREATE TABLE config_revisions (
  id         TEXT PRIMARY KEY,
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  path       TEXT NOT NULL,
  sha256     TEXT NOT NULL,
  content    BLOB NOT NULL,
  user_id    TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX config_revisions_file ON config_revisions(server_id, path, created_at);
