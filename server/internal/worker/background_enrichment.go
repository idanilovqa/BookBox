package worker

import (
	"context"
	"errors"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/config"
	"github.com/google/uuid"
)

type enrichmentMaintenanceRepository interface {
	incompleteBookRepository
	GetBook(context.Context, uuid.UUID) (catalog.BookDetail, error)
}

// QueueMetadataEnrichment revisits identities and missing covers independently
// of scans. Repository cooldowns bound retries after outages or empty searches.
func QueueMetadataEnrichment(ctx context.Context, repo enrichmentMaintenanceRepository, roots []config.Root) error {
	for _, root := range roots {
		if err := queueIncompleteBooks(ctx, repo, root.ID); err != nil {
			return err
		}
		cursor := ""
		for {
			page, err := repo.ListBooks(ctx, catalog.BookQuery{RootID: root.ID, Sort: catalog.BookSortTitle, Limit: bookPageSize, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, book := range page.Items {
				if book.MissingAt != nil || book.ConfirmedMatchID == nil {
					continue
				}
				detail, err := repo.GetBook(ctx, book.ID)
				if errors.Is(err, catalog.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if detail.Book.MissingAt != nil || detail.Book.ConfirmedMatchID == nil {
					continue
				}
				hasCover := false
				for _, file := range detail.Files {
					if file.MissingAt == nil && file.FileKind == catalog.FileKindCover {
						hasCover = true
						break
					}
				}
				if hasCover {
					continue
				}
				match, ok := confirmedMatch(detail.Matches, book.ID, *detail.Book.ConfirmedMatchID)
				if ok {
					// queueCover only needs the enqueue portion of EnrichRepository.
					if err := enqueueMatchCover(ctx, repo, book.ID, match); err != nil {
						return err
					}
				}
			}
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				return errors.New("book pagination cursor did not advance")
			}
			cursor = page.NextCursor
		}
	}
	return nil
}
