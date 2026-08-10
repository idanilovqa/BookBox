create table fingerprint_job_outbox (
    file_id uuid primary key,
    root_id text not null,
    job_kind text not null check (job_kind in ('probe_file', 'inspect_companion')),
    relative_path text not null check (catalog_valid_relative_path(relative_path)),
    expected_size bigint not null check (expected_size >= 0),
    expected_modified_at timestamptz not null check (
        expected_modified_at >= timestamptz '1677-09-21 00:12:43.145225+00'
        and expected_modified_at <= timestamptz '2262-04-11 23:47:16.854775+00'
    ),
    expected_modified_nanos bigint not null check (
        expected_modified_nanos =
            (extract(epoch from expected_modified_at) * 1000000000)::bigint
    ),
    scan_run_id uuid not null,
    dispatch_attempts integer not null default 0 check (dispatch_attempts >= 0),
    next_dispatch_at timestamptz not null default now(),
    last_dispatched_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    foreign key (file_id, root_id) references source_files(id, root_id) on delete cascade,
    foreign key (scan_run_id, root_id) references scan_runs(id, root_id)
);

create index fingerprint_job_outbox_ready_idx
    on fingerprint_job_outbox(next_dispatch_at, file_id);

-- Unix nanoseconds are the fingerprint wire format. Quarantine legacy values
-- which cannot round-trip through Go's time.Time.UnixNano without wrapping.
update source_files
set probe_status='failed',
    probe_error='fingerprint_timestamp_out_of_range',
    probed_at=now(),
    updated_at=now()
where missing_at is null
  and probe_status='pending'
  and (modified_at < timestamptz '1677-09-21 00:12:43.145225+00'
       or modified_at > timestamptz '2262-04-11 23:47:16.854775+00');

-- Recover pending fingerprints from the last successfully published inventory.
-- This creates dormant intent only; workers dispatch it after startup recovery.
insert into fingerprint_job_outbox(
    file_id,root_id,job_kind,relative_path,expected_size,
    expected_modified_at,expected_modified_nanos,scan_run_id,next_dispatch_at
)
select sf.id,sf.root_id,
       case when sf.file_kind='audio' then 'probe_file' else 'inspect_companion' end,
       sf.relative_path,sf.size,sf.modified_at,
       (extract(epoch from sf.modified_at) * 1000000000)::bigint,
       sf.scan_run_id,now()
from source_files sf
join catalog_generations cg
  on cg.root_id=sf.root_id and cg.inventory_scan_run_id=sf.scan_run_id
join scan_runs sr
  on sr.id=sf.scan_run_id and sr.root_id=sf.root_id and sr.status='succeeded'
where sf.missing_at is null
  and sf.probe_status='pending'
  and (sf.file_kind='audio'
       or (sf.file_kind='companion' and sf.companion_kind in ('cue','nfo','opf','playlist')))
  and sf.modified_at >= timestamptz '1677-09-21 00:12:43.145225+00'
  and sf.modified_at <= timestamptz '2262-04-11 23:47:16.854775+00'
on conflict(file_id) do nothing;
