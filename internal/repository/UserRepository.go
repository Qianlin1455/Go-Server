package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	_ "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Users struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	UserName  string    `gorm:"column:username;type:varchar(50);not null;uniqueIndex:uk_users_username"`
	Email     string    `gorm:"column:email;type:varchar(255);not null;uniqueIndex:uk_users_email"`
	Password  string    `gorm:"column:password_hash;type:varchar(255);not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoUpdateTime"`
}

func CreateUser(ctx context.Context, db *gorm.DB, u *Users) error {
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

func GetUserByID(ctx context.Context, db *gorm.DB, id uint64) (*Users, error) {
	user := Users{}
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

func GetUserByName(ctx context.Context, db *gorm.DB, name string) (*Users, error) {
	var user Users
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

func ListUser(ctx context.Context, db *gorm.DB) ([]Users, error) {
	users := make([]Users, 0)

	err := db.WithContext(ctx).Order("id ASC").Find(&users).Error
	if err == nil {
		return users, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrStorage
}
