-- Server membership: a member may see the server and is on its whitelist.
CREATE TABLE server_members (
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  added_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (server_id, user_id)
);
CREATE INDEX server_members_user ON server_members(user_id);

-- Users who joined through a server invite are members of that server.
INSERT OR IGNORE INTO server_members (server_id, user_id, added_by, created_at)
  SELECT whitelist_server_id, used_by, created_by, used_at FROM invites
  WHERE used_by IS NOT NULL AND whitelist_server_id IS NOT NULL;

ALTER TABLE whitelist_queue ADD COLUMN action TEXT NOT NULL DEFAULT 'add' CHECK (action IN ('add', 'remove'));
