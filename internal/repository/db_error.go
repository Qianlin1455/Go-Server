package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// databaseError preserves the cause for internal diagnostics. Only Handler maps
// these errors to public messages; err.Error() must never become an API response.
func databaseError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, errors.Join(ErrStorage, ctx.Err(), err))
	}
	var kind error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		kind = ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		kind = ErrAlreadyExists
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		kind = ErrInvalidReference
	default:
		kind = ErrStorage
	}
	return fmt.Errorf("%s: %w", operation, errors.Join(kind, err))
}
