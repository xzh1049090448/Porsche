-- Disposable test rollback only. Production migrations remain forward-only.
ALTER TABLE users DROP FOREIGN KEY fk_users_business_group;
ALTER TABLE users DROP INDEX idx_users_group_id;
ALTER TABLE users DROP COLUMN group_id;
DROP TABLE business_groups;
