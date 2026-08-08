create extension if not exists pg_trgm;

create function catalog_search_compact(input_value text)
returns text
language sql
immutable
strict
parallel safe
as $$
    select regexp_replace(
        lower(normalize(input_value, NFC)),
        '[^[:alnum:]]',
        '',
        'g'
    )
$$;

create function catalog_search_grams(input_value text)
returns text[]
language sql
immutable
strict
parallel safe
as $$
    select coalesce(array_agg(gram order by gram), '{}'::text[])
    from (
        select distinct substring(compact_value from gram_start for gram_width) as gram
        from (select catalog_search_compact(input_value) as compact_value) compact
        cross join (values (2), (3)) widths(gram_width)
        cross join lateral generate_series(
            1,
            char_length(compact_value) - gram_width + 1
        ) starts(gram_start)
    ) unique_grams
$$;

create table library_search_snapshots (
    root_id text primary key references library_roots(id),
    scan_run_id uuid not null,
    completed_at timestamptz not null,
    document_count integer not null check (document_count >= 0),
    foreign key (scan_run_id, root_id) references scan_runs(id, root_id)
);

create table library_search_documents (
    root_id text not null references library_roots(id),
    document_id uuid not null,
    entity_kind text not null check (entity_kind in ('audiobook', 'folder', 'file')),
    book_id uuid,
    relative_path text not null,
    display_name text not null,
    size bigint check (size is null or size >= 0),
    modified_at timestamptz,
    title text not null default '',
    authors text[] not null default '{}',
    compact_authors text[] not null default '{}',
    series text not null default '',
    narrators text[] not null default '{}',
    genres text[] not null default '{}',
    compact_genres text[] not null default '{}',
    review_state text,
    publication_year integer check (
        publication_year is null or publication_year between 1000 and 3000
    ),
    created_at timestamptz,
    recently_scanned_at timestamptz,
    filenames text[] not null default '{}',
    duration_ms bigint check (duration_ms is null or duration_ms >= 0),
    duplicate_group_id uuid,
    duplicate_count integer not null default 1 check (duplicate_count >= 1),
    compact_name text not null,
    compact_path text not null,
    compact_metadata text not null,
    search_compact text not null,
    search_terms text not null,
    search_grams text[] not null,
    primary key (root_id, document_id),
    foreign key (book_id, root_id) references books(id, root_id),
    check (relative_path = '' or catalog_valid_relative_path(relative_path)),
    check ((entity_kind = 'audiobook' and book_id is not null) or entity_kind <> 'audiobook'),
    check (
        review_state is null
        or review_state in ('unreviewed', 'needs_review', 'confirmed')
    )
);

create index library_search_documents_trgm
    on library_search_documents using gin (search_compact gin_trgm_ops);
create index library_search_documents_grams
    on library_search_documents using gin (search_grams);
create index library_search_documents_author_filter
    on library_search_documents using gin (compact_authors)
    where entity_kind = 'audiobook';
create index library_search_documents_genre_filter
    on library_search_documents using gin (compact_genres)
    where entity_kind = 'audiobook';
create index library_search_documents_page
    on library_search_documents(
        root_id,
        entity_kind,
        display_name,
        relative_path,
        document_id
    );
create index library_search_documents_root_filter
    on library_search_documents(root_id)
    where entity_kind = 'audiobook';
create index library_search_documents_review_filter
    on library_search_documents(review_state)
    where entity_kind = 'audiobook';
create index library_search_documents_year_filter
    on library_search_documents(publication_year)
    where entity_kind = 'audiobook';
create index library_search_documents_title_sort
    on library_search_documents(
        lower(title),
        document_id
    ) where entity_kind = 'audiobook';
create index library_search_documents_author_sort
    on library_search_documents(
        lower(coalesce(authors[1], '')),
        document_id
    ) where entity_kind = 'audiobook';
create index library_search_documents_year_sort
    on library_search_documents(
        coalesce(publication_year, -2147483648) desc,
        document_id desc
    ) where entity_kind = 'audiobook';
create index library_search_documents_added_sort
    on library_search_documents(
        created_at desc,
        document_id desc
    ) where entity_kind = 'audiobook';
create index library_search_documents_scanned_sort
    on library_search_documents(
        coalesce(recently_scanned_at, 'epoch'::timestamptz) desc,
        document_id desc
    ) where entity_kind = 'audiobook';
