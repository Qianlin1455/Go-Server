package repository

import "gorm.io/gorm"

// Paginate 为 GORM 查询添加 LIMIT/OFFSET。
// 前提：page 和 pageSize 已经过合法性及溢出校验。
func Paginate(page, pageSize int) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		offset := (page - 1) * pageSize
		return db.Offset(offset).Limit(pageSize)
	}
}
