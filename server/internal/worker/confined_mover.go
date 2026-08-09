package worker

import (
	"context"
	"errors"

	"bookbox.local/server/internal/catalog"
	"bookbox.local/server/internal/library"
)

type ResolverConfinedMover struct {
	resolver *library.Resolver
}

func (mover *ResolverConfinedMover) InspectDestination(rootID string, moves []catalog.ReorganizationMove) (catalog.ReorganizationDestinationState, error) {
	for _, move := range moves {
		err := mover.Validate(context.Background(), rootID, move)
		if errors.Is(err, catalog.ErrDestinationConflict) {
			return catalog.ReorganizationDestinationState{Exists: true}, nil
		}
		if errors.Is(err, catalog.ErrInvalidData) {
			return catalog.ReorganizationDestinationState{OutsideRoot: true}, nil
		}
		if err != nil {
			return catalog.ReorganizationDestinationState{}, err
		}
	}
	return catalog.ReorganizationDestinationState{}, nil
}

func NewConfinedMover(resolver *library.Resolver) *ResolverConfinedMover {
	return &ResolverConfinedMover{resolver: resolver}
}

func (mover *ResolverConfinedMover) Validate(_ context.Context, rootID string, move catalog.ReorganizationMove) error {
	if mover == nil || mover.resolver == nil {
		return catalog.ErrInvalidData
	}
	err := mover.resolver.ValidateMove(rootID, move.SourcePath, move.DestinationPath, move.ExpectedSize, move.ExpectedModifiedAt)
	if errors.Is(err, library.ErrDestinationExists) {
		return catalog.ErrDestinationConflict
	}
	if errors.Is(err, library.ErrFingerprintChanged) {
		return catalog.ErrReorganizationStale
	}
	if err != nil {
		return catalog.ErrInvalidData
	}
	return nil
}

func (mover *ResolverConfinedMover) Move(_ context.Context, rootID string, move catalog.ReorganizationMove) error {
	if err := mover.resolver.MoveConfined(rootID, move.SourcePath, move.DestinationPath, move.ExpectedSize, move.ExpectedModifiedAt); err != nil {
		if errors.Is(err, library.ErrDestinationExists) {
			return catalog.ErrDestinationConflict
		}
		if errors.Is(err, library.ErrFingerprintChanged) {
			return catalog.ErrReorganizationStale
		}
		return catalog.ErrInvalidData
	}
	return nil
}

func (mover *ResolverConfinedMover) Rollback(_ context.Context, rootID string, move catalog.ReorganizationMove) error {
	return mover.resolver.MoveConfined(rootID, move.DestinationPath, move.SourcePath, move.ExpectedSize, move.ExpectedModifiedAt)
}
