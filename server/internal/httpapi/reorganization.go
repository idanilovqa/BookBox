package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"bookbox.local/server/internal/catalog"
	"github.com/google/uuid"
)

type ReorganizationCatalog interface {
	BuildReorganizationPreview(context.Context, catalog.ReorganizationRequest) (catalog.ReorganizationPreview, error)
	QueueReorganization(context.Context, uuid.UUID, string) (catalog.WorkerJob, error)
}

type ReorganizationAccess struct {
	Enabled               bool
	HasCapability         func(string, string) bool
	ProbeBookRootWritable func(context.Context, uuid.UUID) error
}

type reorganizationPreviewRequest struct {
	CatalogGeneration int64                   `json:"catalogGeneration"`
	Groups            []catalog.GroupingGroup `json:"groups"`
	TargetFolders     map[string]string       `json:"targetFolders"`
}

type reorganizationConfirmRequest struct {
	OperationID string `json:"operationId"`
}

func reorganizationAllowed(ctx context.Context, access ReorganizationAccess, actor string, bookID uuid.UUID) bool {
	return access.Enabled && access.HasCapability != nil &&
		access.HasCapability(actor, "library:reorganize") &&
		access.ProbeBookRootWritable != nil && access.ProbeBookRootWritable(ctx, bookID) == nil
}

func reorganizationPreviewHandler(repository ReorganizationCatalog, access ReorganizationAccess) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		bookID, err := uuid.Parse(request.PathValue("bookId"))
		actor, authenticated := accessSubject(request.Context())
		if err != nil || bookID == uuid.Nil {
			writeStableError(response, http.StatusBadRequest, "invalid_book_id", "book identifier is invalid")
			return
		}
		if repository == nil || !authenticated || !reorganizationAllowed(request.Context(), access, actor, bookID) {
			writeStableError(response, http.StatusForbidden, "library_reorganization_disabled", "library reorganization is disabled")
			return
		}
		var input reorganizationPreviewRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		preview, err := repository.BuildReorganizationPreview(request.Context(), catalog.ReorganizationRequest{
			BookID: bookID, CatalogGeneration: input.CatalogGeneration,
			Groups: input.Groups, TargetFolders: input.TargetFolders, Actor: actor,
		})
		if err != nil {
			writeReorganizationError(response, err)
			return
		}
		moves := make([]map[string]any, 0, len(preview.Moves))
		for _, move := range preview.Moves {
			moves = append(moves, map[string]any{
				"order": move.Order, "fileId": move.FileID.String(),
				"sourcePath": move.SourcePath, "destinationPath": move.DestinationPath,
				"role": move.Role, "expectedSize": move.ExpectedSize,
				"expectedModifiedAt": move.ExpectedModifiedAt,
			})
		}
		writeJSON(response, http.StatusOK, map[string]any{
			"operationId": preview.OperationID.String(), "rootId": preview.RootID,
			"catalogGeneration": preview.CatalogGeneration, "moves": moves,
		})
	})
}

func reorganizationConfirmHandler(repository ReorganizationCatalog, access ReorganizationAccess) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		bookID, err := uuid.Parse(request.PathValue("bookId"))
		actor, authenticated := accessSubject(request.Context())
		if err != nil || bookID == uuid.Nil {
			writeStableError(response, http.StatusBadRequest, "invalid_book_id", "book identifier is invalid")
			return
		}
		if repository == nil || !authenticated || !reorganizationAllowed(request.Context(), access, actor, bookID) {
			writeStableError(response, http.StatusForbidden, "library_reorganization_disabled", "library reorganization is disabled")
			return
		}
		var input reorganizationConfirmRequest
		if !decodeCatalogJSON(response, request, &input) {
			return
		}
		operationID, err := uuid.Parse(strings.TrimSpace(input.OperationID))
		if err != nil || operationID == uuid.Nil {
			writeInvalidRequest(response)
			return
		}
		job, err := repository.QueueReorganization(request.Context(), operationID, actor)
		if err != nil {
			writeReorganizationError(response, err)
			return
		}
		writeJSON(response, http.StatusAccepted, acceptedJobResponse{JobID: job.ID.String(), Status: "queued"})
	})
}

func writeReorganizationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrReorganizationStale):
		writeStableError(response, http.StatusConflict, "reorganization_stale", "reorganization preview is stale")
	case errors.Is(err, catalog.ErrDestinationConflict):
		writeStableError(response, http.StatusConflict, "destination_conflict", "reorganization destination conflicts")
	case errors.Is(err, catalog.ErrInvalidData), errors.Is(err, catalog.ErrInvalidPath):
		writeInvalidRequest(response)
	default:
		writeInternalError(response)
	}
}
