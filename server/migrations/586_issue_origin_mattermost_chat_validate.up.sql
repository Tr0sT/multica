-- Validate the widened CHECK separately from migration 573 so the validation
-- scan does not inherit migration 573's ACCESS EXCLUSIVE lock.
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
