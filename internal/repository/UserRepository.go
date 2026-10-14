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
	if result.Error == nil {
		return nil
	}
	// 优先处理 Context 取消或超时
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 唯一键冲突
	if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
		return ErrAlreadyExists
	}
	// 其他数据库错误
	return fmt.Errorf("create user failed: %w", ErrStorage)
}

func GetUserByID(ctx context.Context, db *gorm.DB, id uint64) (*model.Users, error) {
	user := model.Users{}
	result := db.WithContext(ctx).First(&user, id)
	if result.Error == nil {
		return &user, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("没有找到记录:%w", ErrNotFound)
	}

	return nil, fmt.Errorf("查询失败:%w", ErrStorage)
}

func GetUserByName(ctx context.Context, db *gorm.DB, name string) (*model.Users, error) {
	var user model.Users
	result := db.WithContext(ctx).Where("username = ?", name).First(&user)
	if result.Error == nil {
		return &user, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}

	return nil, ErrStorage
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
			return nil, ctx.Err()
		}

		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}

		return nil, fmt.Errorf("get user by email: %w", err)
	}

	return &user, nil
}

func ListUser(ctx context.Context, db *gorm.DB) ([]model.Users, error) {
	users := make([]model.Users, 0)

	err := db.WithContext(ctx).Order("id ASC").Find(&users).Error
	if err == nil {
		return users, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrStorage
}
