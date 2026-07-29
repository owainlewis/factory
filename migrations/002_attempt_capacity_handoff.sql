ALTER TABLE attempts
ADD COLUMN capacity_acknowledged INTEGER NOT NULL DEFAULT 0
CHECK (capacity_acknowledged IN (0, 1));

CREATE INDEX attempts_unacknowledged_capacity
ON attempts(worker_id, capacity_acknowledged, state);
