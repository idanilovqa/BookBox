package worker

import (
	"context"
	"errors"
	"strings"

	"bookbox.local/server/internal/catalog"
	"github.com/google/uuid"
)

type ReorganizationRepository interface {
	LoadReorganization(context.Context, uuid.UUID, string) (catalog.ReorganizationOperation, error)
	SetReorganizationStatus(context.Context, uuid.UUID, string, string) error
	SetReorganizationMoveStatus(context.Context, uuid.UUID, int, string) error
	CommitReorganizationGrouping(context.Context, catalog.ReorganizationOperation) error
}

type ConfinedMover interface {
	Validate(context.Context, string, catalog.ReorganizationMove) error
	Move(context.Context, string, catalog.ReorganizationMove) error
	Rollback(context.Context, string, catalog.ReorganizationMove) error
}

type ReorganizeFilesHandler struct {
	Repository ReorganizationRepository
	Mover      ConfinedMover
}

var errReorganizationFailed = errors.New("reorganization failed")

func (handler ReorganizeFilesHandler) Handle(ctx context.Context, job catalog.WorkerJob) error {
	if handler.Repository == nil || handler.Mover == nil {
		return errors.New("reorganization dependencies are unavailable")
	}
	operationID, err := requiredPayloadUUID(job.Payload, "operationId")
	if err != nil {
		return err
	}
	actor, err := requiredPayloadString(job.Payload, "actor")
	if err != nil {
		return err
	}
	operation, err := handler.Repository.LoadReorganization(ctx, operationID, actor)
	if err != nil {
		return err
	}
	if err := handler.Repository.SetReorganizationStatus(ctx, operation.ID, "running", ""); err != nil {
		return err
	}
	moved := make([]catalog.ReorganizationMove, 0, len(operation.Moves))
	fail := func(cause error) error {
		needsAttention := false
		unresolved := make([]string, 0)
		for index := len(moved) - 1; index >= 0; index-- {
			move := moved[index]
			if rollbackErr := handler.Mover.Rollback(context.WithoutCancel(ctx), operation.RootID, move); rollbackErr != nil {
				needsAttention = true
				unresolved = append(unresolved, move.DestinationPath)
				_ = handler.Repository.SetReorganizationMoveStatus(context.WithoutCancel(ctx), operation.ID, move.Order, "failed")
				continue
			}
			_ = handler.Repository.SetReorganizationMoveStatus(context.WithoutCancel(ctx), operation.ID, move.Order, "rolled_back")
		}
		status := "rolled_back"
		if needsAttention {
			status = "needs_attention"
		}
		errorText := "reorganization failed"
		if len(unresolved) > 0 {
			errorText = "rollback incomplete: " + strings.Join(unresolved, ", ")
		}
		_ = handler.Repository.SetReorganizationStatus(context.WithoutCancel(ctx), operation.ID, status, errorText)
		_ = cause
		return errReorganizationFailed
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	for _, move := range operation.Moves {
		if err := handler.Mover.Validate(ctx, operation.RootID, move); err != nil {
			return fail(err)
		}
		if err := handler.Mover.Move(ctx, operation.RootID, move); err != nil {
			return fail(err)
		}
		moved = append(moved, move)
		if err := handler.Repository.SetReorganizationMoveStatus(ctx, operation.ID, move.Order, "moved"); err != nil {
			return fail(err)
		}
	}
	if err := handler.Repository.CommitReorganizationGrouping(ctx, operation); err != nil {
		return fail(err)
	}
	return nil
}
