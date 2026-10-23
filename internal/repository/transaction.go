package repository

import (
	"context"

	"gorm.io/gorm"
)

// Transaction keeps business decisions in the caller while normalizing errors
// from BEGIN/COMMIT, including drivers returning sql.ErrTxDone after a timeout.
func Transaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	var callbackErr error
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		callbackErr = fn(tx)
		if callbackErr == nil {
			callbackErr = ctx.Err()
		}
		return callbackErr
	})
	if callbackErr != nil {
		return callbackErr
	}
	return databaseError(ctx, "transaction", err)
}
