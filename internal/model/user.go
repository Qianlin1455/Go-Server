package model

import "time"

type Users struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	UserName  string    `gorm:"column:username;type:varchar(50);not null;uniqueIndex:uk_model.Users_username"`
	Email     string    `gorm:"column:email;type:varchar(255);not null;uniqueIndex:uk_model.Users_email"`
	Password  string    `gorm:"column:password_hash;type:varchar(255);not null"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(3);not null;default:CURRENT_TIMESTAMP(3);autoUpdateTime"`
}
