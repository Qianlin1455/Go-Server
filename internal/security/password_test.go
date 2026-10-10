package security

import (
	"testing"
)

func TestPassword(t *testing.T) {
	password := "Alice@123456"

	// 1. 生成哈希
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}

	// 2. 验证正确密码
	if err := CheckPassword(hash, password); err != nil {
		t.Fatalf("正确密码验证失败: %v", err)
	}

	// TODO 3: 验证错误密码不能通过
	password1 := "123456"
	if err := CheckPassword(hash, password1); err == nil {
		t.Fatalf("错误密码验证成功：%v", err)
	}
	// TODO 4: 对相同密码再次生成 hash，
	hash1, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 == hash {
		t.Fatal("两次hash一致")
	}
	// 检查两次 hash 是否不同
}

func TestPasswordTooLong(t *testing.T) {
	password := string(make([]byte, 73))

	_, err := HashPassword(password)
	if err == nil {
		t.Fatal("超过72字节的密码应该被拒绝")
	}
}
