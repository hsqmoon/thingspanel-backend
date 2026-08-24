CREATE TABLE IF NOT EXISTS telemetry_hourly (
  device_id varchar(36) NOT NULL,
  "key" varchar(255) NOT NULL,
  bucket_ts bigint NOT NULL,
  avg_number double precision,
  min_number double precision,
  max_number double precision,
  last_number double precision,
  last_bool boolean,
  last_string text,
  sample_count bigint NOT NULL,
  PRIMARY KEY(device_id,"key",bucket_ts)
);
CREATE INDEX IF NOT EXISTS telemetry_hourly_bucket_idx ON telemetry_hourly(bucket_ts DESC);
CREATE TABLE IF NOT EXISTS telemetry_daily (LIKE telemetry_hourly INCLUDING ALL);
