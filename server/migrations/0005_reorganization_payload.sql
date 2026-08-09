alter table reorganization_operations
    add column groups jsonb not null default '[]'::jsonb
        check (jsonb_typeof(groups) = 'array'),
    add column target_folders jsonb not null default '{}'::jsonb
        check (jsonb_typeof(target_folders) = 'object');
