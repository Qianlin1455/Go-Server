package database

import "testing"

func TestInitDB(t *testing.T) {
	db, err := InitDB()
	if err != nil {
		t.Fatalf("数据库连接失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	defer sqlDB.Close()

	t.Log("MySQL 数据库连接成功")
}
