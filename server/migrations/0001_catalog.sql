create extension if not exists pg_trgm;

create table if not exists schema_migrations (
    version bigint primary key,
    applied_at timestamptz not null default now()
);

create function catalog_valid_relative_path(value text)
returns boolean
language sql
immutable
strict
as $$
    select value <> ''
       and value <> '.'
       and left(value, 1) <> '/'
       and value !~ '^[A-Za-z]:/'
       and position(chr(92) in value) = 0
       and value !~ '[[:cntrl:]]'
       and not (string_to_array(value, '/') && array['', '.', '..']::text[])
$$;

create table library_roots (
    id text primary key,
    display_name text not null check (btrim(display_name) <> ''),
    scanned_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table scan_runs (
    id uuid primary key,
    root_id text not null references library_roots(id),
    started_at timestamptz not null,
    finished_at timestamptz,
    status text not null check (status in ('running', 'succeeded', 'failed', 'partial')),
    error text,
    unique (id, root_id),
    check (
        (status = 'running' and finished_at is null)
        or (status <> 'running' and finished_at is not null and finished_at >= started_at)
    )
);
create unique index scan_runs_one_running_per_root
    on scan_runs(root_id)
    where status = 'running';

create table source_files (
    id uuid primary key,
    root_id text not null references library_roots(id),
    relative_path text not null check (catalog_valid_relative_path(relative_path)),
    size bigint not null check (size >= 0),
    modified_at timestamptz not null,
    sha256 text check (sha256 is null or sha256 ~ '^[0-9a-f]{64}$'),
    media jsonb not null default '{}'::jsonb check (jsonb_typeof(media) = 'object'),
    scan_run_id uuid,
    missing_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (root_id, relative_path),
    unique (id, root_id),
    foreign key (scan_run_id, root_id) references scan_runs(id, root_id)
);

create table books (
    id uuid primary key,
    root_id text not null references library_roots(id),
    source_folder text not null check (
        source_folder = '' or catalog_valid_relative_path(source_folder)
    ),
    grouping_key text not null check (btrim(grouping_key) <> '' and grouping_key !~ '[[:cntrl:]]'),
    grouping_locked boolean not null default false,
    title text not null check (btrim(title) <> ''),
    subtitle text,
    authors text[] not null default '{}',
    narrators text[] not null default '{}',
    series text,
    series_position text,
    publisher text,
    published_date text,
    publication_year integer check (publication_year is null or publication_year between 1000 and 3000),
    genres text[] not null default '{}',
    provider_subjects text[] not null default '{}',
    identifiers jsonb not null default '{}'::jsonb check (jsonb_typeof(identifiers) = 'object'),
    metadata_source text not null check (
        metadata_source in ('inferred', 'local', 'open_library', 'google_books', 'confirmed', 'mixed')
    ),
    confidence numeric(4,3) not null check (confidence between 0 and 1),
    review_state text not null check (review_state in ('unreviewed', 'needs_review', 'confirmed')),
    confirmed_match_id uuid,
    missing_at timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    unique (root_id, source_folder, grouping_key),
    unique (id, root_id)
);

create table book_files (
    book_id uuid not null,
    file_id uuid not null,
    root_id text not null,
    role text not null check (role in ('audio', 'cover', 'sidecar')),
    primary key (book_id, file_id),
    foreign key (book_id, root_id) references books(id, root_id) on delete cascade,
    foreign key (file_id, root_id) references source_files(id, root_id)
);

create table grouping_corrections (
    id uuid primary key,
    root_id text not null references library_roots(id),
    source_folder text not null check (
        source_folder = '' or catalog_valid_relative_path(source_folder)
    ),
    groups jsonb not null check (jsonb_typeof(groups) = 'array'),
    updated_at timestamptz not null default now(),
    unique (root_id, source_folder)
);

create table metadata_matches (
    id uuid primary key,
    book_id uuid not null references books(id) on delete cascade,
    provider text not null check (provider in ('open_library', 'google_books')),
    provider_id text not null check (btrim(provider_id) <> ''),
    score numeric(4,3) not null check (score between 0 and 1),
    payload jsonb not null check (jsonb_typeof(payload) = 'object'),
    cover_url text,
    confirmed boolean not null default false,
    created_at timestamptz not null default now(),
    unique (book_id, provider, provider_id),
    unique (id, book_id)
);

alter table books
    add constraint books_confirmed_match_same_book_fk
    foreign key (confirmed_match_id, id)
    references metadata_matches(id, book_id)
    deferrable initially deferred;

create unique index metadata_matches_one_confirmed_per_book
    on metadata_matches(book_id)
    where confirmed;

create table cover_events (
    id uuid primary key,
    book_id uuid not null references books(id),
    action text not null check (action in ('created', 'replaced')),
    destination text not null check (catalog_valid_relative_path(destination)),
    old_sha256 text check (old_sha256 is null or old_sha256 ~ '^[0-9a-f]{64}$'),
    new_sha256 text not null check (new_sha256 ~ '^[0-9a-f]{64}$'),
    old_dimensions integer[],
    new_dimensions integer[] not null,
    provider text not null check (btrim(provider) <> ''),
    backup_key text,
    created_at timestamptz not null default now(),
    check (
        old_dimensions is null
        or (
            cardinality(old_dimensions) = 2
            and old_dimensions[1] > 0
            and old_dimensions[2] > 0
        )
    ),
    check (
        cardinality(new_dimensions) = 2
        and new_dimensions[1] > 0
        and new_dimensions[2] > 0
    )
);

create table worker_jobs (
    id uuid primary key,
    kind text not null check (kind in ('scan_root', 'enrich_book', 'publish_cover')),
    payload jsonb not null check (jsonb_typeof(payload) = 'object'),
    status text not null default 'queued' check (status in ('queued', 'running', 'succeeded', 'dead')),
    run_after timestamptz not null default now(),
    attempts integer not null default 0 check (attempts >= 0),
    max_attempts integer not null default 5 check (max_attempts between 1 and 100),
    locked_at timestamptz,
    locked_by text,
    lease_until timestamptz,
    lease_token uuid,
    last_error text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    check (
        (
            status = 'running'
            and locked_at is not null
            and locked_by is not null
            and lease_until >= locked_at
            and lease_token is not null
        )
        or (
            status <> 'running'
            and locked_at is null
            and locked_by is null
            and lease_until is null
            and lease_token is null
        )
    ),
    check (attempts <= max_attempts)
);

create table availability_events (
    id uuid primary key,
    root_id text not null references library_roots(id),
    entity_kind text not null check (entity_kind in ('source_file', 'book')),
    entity_id uuid not null,
    action text not null check (action in ('missing', 'restored')),
    scan_run_id uuid,
    created_at timestamptz not null default now(),
    foreign key (scan_run_id, root_id) references scan_runs(id, root_id)
);

create index source_files_scan_run on source_files(root_id, scan_run_id);
create index source_files_present on source_files(root_id, relative_path) where missing_at is null;
create index books_title_trgm on books using gin (title gin_trgm_ops);
create function catalog_book_search_document(
    title text,
    authors text[],
    series text,
    narrators text[],
    genres text[]
)
returns tsvector
language sql
immutable
as $$
    select to_tsvector(
        'simple'::regconfig,
        coalesce(title, '') || ' ' ||
        array_to_string(authors, ' ') || ' ' ||
        coalesce(series, '') || ' ' ||
        array_to_string(narrators, ' ') || ' ' ||
        array_to_string(genres, ' ')
    )
$$;
create index books_search on books using gin (
    catalog_book_search_document(title, authors, series, narrators, genres)
);
create index worker_jobs_claim
    on worker_jobs(run_after, created_at, id)
    where status = 'queued';
create index availability_events_entity
    on availability_events(entity_kind, entity_id, created_at);
