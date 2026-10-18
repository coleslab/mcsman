-- Per-server overrides. NULL means "use the daemon default".
ALTER TABLE servers ADD COLUMN backup_schedule TEXT;
ALTER TABLE servers ADD COLUMN backup_keep INTEGER;
ALTER TABLE servers ADD COLUMN restart_schedule TEXT;
ALTER TABLE servers ADD COLUMN autostart INTEGER NOT NULL DEFAULT 0;
