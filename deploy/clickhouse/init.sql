-- The Collector's clickhouse exporter creates its tables (create_schema: true)
-- but, in the versions pinned here, cannot create the database it connects to.
CREATE DATABASE IF NOT EXISTS otel;
