package repository

import (
	"GoServer/internal/model"
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func CreateUser(ctx context.Context, db *gorm.DB, u *model.Users) error {
	result := db.WithContext(ctx).Create(u)
	return databaseError(ctx, "create user", result.Error)
}

func GetUserByID(ctx context.Context, db *gorm.DB, id uint64) (*model.Users, error) {
	user := model.Users{}
	result := db.WithContext(ctx).First(&user, id)
	if result.Error != nil {
		return nil, databaseError(ctx, "get user by id", result.Error)
	}
	return &user, nil
}

func GetUserByName(ctx context.Context, db *gorm.DB, name string) (*model.Users, error) {
	var user model.Users
	result := db.WithContext(ctx).Where("username = ?", name).First(&user)
	if result.Error != nil {
		return nil, databaseError(ctx, "get user by name", result.Error)
	}
	return &user, nil
}

func GetUserByEmail(
	ctx context.Context,
	db *gorm.DB,
	email string,
) (*model.Users, error) {

	var user model.Users

	err := db.WithContext(ctx).
		Where("email = ?", email).
		First(&user).Error

	if err != nil {
		if ctx.Err() != nil {
			return nil, databaseError(ctx, "query user", err)
		}

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("get user by email: %w", errors.Join(ErrUserNotFound, err))
		}

		return nil, databaseError(ctx, "get user by email", err)
	}

	return &user, nil
}

func ListUser(ctx context.Context, db *gorm.DB) ([]model.Users, error) {
	users := make([]model.Users, 0)

	err := db.WithContext(ctx).Order("id ASC").Find(&users).Error
	if err == nil {
		return users, nil
	}
	return nil, databaseError(ctx, "list users", err)
}
