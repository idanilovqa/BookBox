-- Current catalog status, not a historical failed-job listing. One row per
-- available book with live audio; independent physical copies stay separate.
COPY (
 WITH current_files AS (
  SELECT bf.book_id, count(*) FILTER (WHERE bf.role='audio') AS audio_files,
   bool_or(bf.role='cover') AS cover_indexed
  FROM book_files bf JOIN source_files sf ON sf.id=bf.file_id
  WHERE sf.missing_at IS NULL GROUP BY bf.book_id
 ), latest_jobs AS (
  SELECT DISTINCT ON (payload->>'bookId') payload->>'bookId' AS book_id,
   status,attempts,last_error,updated_at
  FROM worker_jobs WHERE kind='enrich_book'
  ORDER BY payload->>'bookId',created_at DESC,id DESC
 ), published_covers AS (SELECT DISTINCT book_id FROM cover_events)
 SELECT b.id AS book_id,b.title,array_to_string(b.authors,'; ') AS authors,
  b.root_id,b.source_folder,f.audio_files,
  b.confirmed_match_id IS NOT NULL AS metadata_confirmed,
  f.cover_indexed,p.book_id IS NOT NULL AS cover_published,
  CASE WHEN b.confirmed_match_id IS NULL THEN 'needs_metadata'
   WHEN NOT f.cover_indexed THEN 'needs_cover'
   ELSE 'complete' END AS current_status,
  j.status AS latest_job_status,j.attempts AS latest_job_attempts,
  j.last_error AS latest_job_error,j.updated_at AS latest_job_updated_at
 FROM books b JOIN current_files f ON f.book_id=b.id AND f.audio_files>0
 LEFT JOIN latest_jobs j ON j.book_id=b.id::text
 LEFT JOIN published_covers p ON p.book_id=b.id
 WHERE b.missing_at IS NULL
 ORDER BY lower(b.title),b.source_folder,b.id
) TO STDOUT WITH (FORMAT CSV, HEADER TRUE);
