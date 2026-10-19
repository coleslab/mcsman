DELETE FROM whitelist_queue WHERE action = 'remove';
ALTER TABLE whitelist_queue DROP COLUMN action;
DROP TABLE server_members;
