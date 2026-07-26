alter table worker_jobs
    add column dedupe_key text check (
        dedupe_key is null
        or (length(dedupe_key) between 1 and 256 and dedupe_key !~ '[[:cntrl:]]')
    );

create unique index worker_jobs_active_dedupe
    on worker_jobs(kind, dedupe_key)
    where dedupe_key is not null and status in ('queued', 'running');
