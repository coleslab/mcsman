-- Let users be deleted without losing world import history.
CREATE TABLE world_imports_new (
  id                   TEXT PRIMARY KEY,
  server_id            TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  imported_at          INTEGER NOT NULL,
  imported_by          TEXT REFERENCES users(id) ON DELETE SET NULL,
  source_name          TEXT NOT NULL,
  source_mc_version    TEXT NOT NULL,
  pre_import_backup_id TEXT REFERENCES backups(id) ON DELETE SET NULL
);
INSERT INTO world_imports_new SELECT * FROM world_imports;
DROP TABLE world_imports;
ALTER TABLE world_imports_new RENAME TO world_imports;

CREATE INDEX idx_whitelist_pending ON whitelist_queue(server_id) WHERE applied_at IS NULL;

-- Invites created over the CLI socket have no creating user.
CREATE TABLE invites_new (
  id                  TEXT PRIMARY KEY,
  token_hash          TEXT NOT NULL UNIQUE,
  created_by          TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at          INTEGER NOT NULL,
  expires_at          INTEGER NOT NULL,
  used_at             INTEGER,
  used_by             TEXT REFERENCES users(id) ON DELETE SET NULL,
  whitelist_server_id TEXT REFERENCES servers(id) ON DELETE SET NULL
);
INSERT INTO invites_new SELECT * FROM invites;
DROP TABLE invites;
ALTER TABLE invites_new RENAME TO invites;
