-- Terminal enrichment retries inspect the latest job, including completed jobs.
-- The active-job uniqueness index cannot serve this unrestricted lookup.
create index if not exists worker_jobs_dedupe_history
    on worker_jobs(kind, dedupe_key, created_at desc, id desc)
    where dedupe_key is not null;