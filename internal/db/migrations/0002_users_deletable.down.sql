DROP INDEX idx_whitelist_pending;
CREATE TABLE world_imports_old (
  id                   TEXT PRIMARY KEY,
  server_id            TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  imported_at          INTEGER NOT NULL,
  imported_by          TEXT NOT NULL REFERENCES users(id),
  source_name          TEXT NOT NULL,
  source_mc_version    TEXT NOT NULL,
  pre_import_backup_id TEXT REFERENCES backups(id) ON DELETE SET NULL
);
INSERT INTO world_imports_old SELECT * FROM world_imports WHERE imported_by IS NOT NULL;
DROP TABLE world_imports;
ALTER TABLE world_imports_old RENAME TO world_imports;

CREATE TABLE invites_old (
  id                  TEXT PRIMARY KEY,
  token_hash          TEXT NOT NULL UNIQUE,
  created_by          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at          INTEGER NOT NULL,
  expires_at          INTEGER NOT NULL,
  used_at             INTEGER,
  used_by             TEXT REFERENCES users(id) ON DELETE SET NULL,
  whitelist_server_id TEXT REFERENCES servers(id) ON DELETE SET NULL
);
INSERT INTO invites_old SELECT * FROM invites WHERE created_by IS NOT NULL;
DROP TABLE invites;
ALTER TABLE invites_old RENAME TO invites;
