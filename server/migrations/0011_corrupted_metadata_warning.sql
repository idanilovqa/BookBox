alter table book_grouping_evidence
    drop constraint book_grouping_evidence_reason_code_check;

alter table book_grouping_evidence
    add constraint book_grouping_evidence_reason_code_check
    check (reason_code in (
        'folder_default', 'consistent_subtree', 'distinct_identity',
        'conflicting_title', 'conflicting_author', 'weak_loose_files',
        'probe_failed', 'manual', 'reorganization_incomplete',
        'corrupted_metadata'
    ));

alter table library_search_documents
    drop constraint library_search_documents_review_state_reason_check;

alter table library_search_documents
    add constraint library_search_documents_review_state_reason_check
    check (
        (
            entity_kind = 'audiobook'
            and (
                (review_state = 'needs_review' and review_reason is not null)
                or (review_state = 'confirmed' and (review_reason is null or review_reason = 'corrupted_metadata'))
                or (review_state = 'unreviewed' and review_reason is null)
            )
        )
        or (
            entity_kind in ('folder', 'file')
            and review_state is null
            and review_reason is null
        )
    );
