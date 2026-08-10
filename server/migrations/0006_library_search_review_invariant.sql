-- Raw folder/file documents do not carry audiobook review metadata. Normalize
-- any legacy projection rows before enforcing that distinction.
update library_search_documents
set review_state = null,
    review_reason = null
where entity_kind in ('folder', 'file')
  and (review_state is not null or review_reason is not null);

-- Earlier projections allowed audiobook review state to be null and added the
-- reason column without backfilling it. Rebuild those values from the catalog
-- records that own the projection document.
update library_search_documents as documents
set review_state = books.review_state,
    review_reason = case
        when books.review_state = 'needs_review' then coalesce(
            nullif(documents.review_reason, ''),
            (
                select evidence.reason_code
                from book_grouping_evidence as evidence
                where evidence.book_id = books.id
                  and evidence.root_id = books.root_id
            ),
            'legacy_review_state'
        )
        else null
    end
from books
where documents.entity_kind = 'audiobook'
  and documents.book_id = books.id
  and documents.root_id = books.root_id;

alter table library_search_documents
    add constraint library_search_documents_review_state_reason_check
    check (
        (
            entity_kind = 'audiobook'
            and review_state is not null
            and ((review_state = 'needs_review') = (review_reason is not null))
        )
        or (
            entity_kind in ('folder', 'file')
            and review_state is null
            and review_reason is null
        )
    );
