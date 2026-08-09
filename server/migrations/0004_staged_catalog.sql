alter table source_files
    add column file_kind text not null default 'audio'
        constraint source_files_file_kind_check
        check (file_kind in ('audio', 'cover', 'companion')),
    add column companion_kind text
        constraint source_files_companion_kind_check
        check (companion_kind is null or companion_kind in (
            'cue', 'nfo', 'opf', 'playlist', 'ebook', 'other'
        )),
    add column probe_status text not null default 'pending'
        constraint source_files_probe_status_check
        check (probe_status in ('not_applicable', 'pending', 'succeeded', 'failed')),
    add column probe_version integer not null default 0
        constraint source_files_probe_version_check
        check (probe_version >= 0),
    add column probe_error text,
    add column probed_at timestamptz;

-- Migration 0003 catalogs were produced by the probing scanner. Preserve that
-- state instead of making every existing file look like an unprobed audio file.
update source_files as files
set file_kind = case
        when media->>'role' = 'cover' or exists (
            select 1 from book_files
            where file_id = files.id and root_id = files.root_id and role = 'cover'
        ) then 'cover'
        else 'audio'
    end,
    probe_status = case
        when media->>'role' = 'cover' or exists (
            select 1 from book_files
            where file_id = files.id and root_id = files.root_id and role = 'cover'
        ) then 'not_applicable'
        when nullif(media->>'probeError', '') is not null then 'failed'
        else 'succeeded'
    end,
    probe_version = case
        when media->>'role' = 'cover' or exists (
            select 1 from book_files
            where file_id = files.id and root_id = files.root_id and role = 'cover'
        ) then 0
        else 1
    end,
    probe_error = nullif(media->>'probeError', ''),
    probed_at = case
        when media->>'role' = 'cover' or exists (
            select 1 from book_files
            where file_id = files.id and root_id = files.root_id and role = 'cover'
        ) then null
        else updated_at
    end;

create table catalog_generations (
    root_id text primary key references library_roots(id),
    generation bigint not null default 1 check (generation > 0),
    inventory_scan_run_id uuid,
    updated_at timestamptz not null default now(),
    foreign key (inventory_scan_run_id, root_id) references scan_runs(id, root_id)
);

insert into catalog_generations(root_id, generation, inventory_scan_run_id, updated_at)
select roots.id,
       1,
       snapshots.scan_run_id,
       coalesce(snapshots.completed_at, now())
from library_roots roots
left join library_search_snapshots snapshots on snapshots.root_id = roots.id;

alter table library_search_snapshots
    add column catalog_generation bigint not null default 1
        constraint library_search_snapshots_catalog_generation_check
        check (catalog_generation > 0);

alter table library_search_documents
    add column review_reason text;

create table book_grouping_evidence (
    book_id uuid primary key references books(id) on delete cascade,
    root_id text not null,
    reason_code text not null check (reason_code in (
        'folder_default', 'consistent_subtree', 'distinct_identity',
        'conflicting_title', 'conflicting_author', 'weak_loose_files',
        'probe_failed', 'manual', 'reorganization_incomplete'
    )),
    evidence jsonb not null check (jsonb_typeof(evidence) = 'object'),
    updated_at timestamptz not null default now(),
    foreign key (book_id, root_id) references books(id, root_id) on delete cascade
);

alter table worker_jobs drop constraint worker_jobs_kind_check;
alter table worker_jobs add constraint worker_jobs_kind_check check (kind in (
    'scan_root', 'probe_file', 'inspect_companion', 'regroup_scope',
    'publish_search', 'enrich_book', 'publish_cover', 'reorganize_files'
));

create table reorganization_operations (
    id uuid primary key,
    root_id text not null references library_roots(id),
    book_id uuid not null,
    catalog_generation bigint not null check (catalog_generation > 0),
    actor text not null check (btrim(actor) <> ''),
    status text not null check (status in (
        'previewed', 'queued', 'running', 'succeeded', 'rolled_back', 'needs_attention'
    )),
    last_error text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    foreign key (book_id, root_id) references books(id, root_id)
);

create table reorganization_moves (
    operation_id uuid not null references reorganization_operations(id) on delete cascade,
    move_order integer not null check (move_order >= 0),
    file_id uuid not null,
    root_id text not null,
    source_path text not null check (catalog_valid_relative_path(source_path)),
    destination_path text not null check (catalog_valid_relative_path(destination_path)),
    role text not null check (role in ('audio', 'cover', 'sidecar')),
    expected_size bigint not null check (expected_size >= 0),
    expected_modified_at timestamptz not null,
    state text not null check (state in ('planned', 'moved', 'rolled_back', 'failed')),
    last_error text,
    primary key (operation_id, move_order),
    unique (operation_id, source_path),
    unique (operation_id, destination_path),
    foreign key (file_id, root_id) references source_files(id, root_id)
);
