alter table library_search_documents
    drop constraint library_search_documents_review_state_reason_check;

alter table library_search_documents
    add constraint library_search_documents_review_state_reason_check
    check ((
        (
            entity_kind = 'audiobook'
            and (
                (review_state = 'needs_review' and review_reason is not null)
                or (review_state in ('confirmed', 'unreviewed') and (
                    review_reason is null or review_reason = 'corrupted_metadata'
                ))
            )
        )
        or (
            entity_kind in ('folder', 'file')
            and review_state is null
            and review_reason is null
        )
    ) is true);
