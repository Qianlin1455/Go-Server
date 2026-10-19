package service

import (
	"errors"
	"strings"
	"testing"
)

func TestCreateProject_InvalidInput(t *testing.T) {
	svc := NewProjectService(nil)

	tests := []struct {
		name   string
		userID uint64
		input  CreateProjectInput
	}{
		{
			name:   "用户 ID 为零",
			userID: 0,
			input: CreateProjectInput{
				Name: "valid project",
			},
		},
		{
			name:   "项目名称为空",
			userID: 1,
			input: CreateProjectInput{
				Name: "",
			},
		},
		{
			name:   "项目名称只有空白",
			userID: 1,
			input: CreateProjectInput{
				Name: "   ",
			},
		},
		{
			name:   "项目名称超过 128 个字符",
			userID: 1,
			input: CreateProjectInput{
				Name: strings.Repeat("a", 129),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := svc.CreateProject(
				t.Context(),
				tt.userID,
				tt.input,
			)

			if !errors.Is(err, ErrInvalidProjectInput) {
				t.Fatalf(
					"期望 ErrInvalidProjectInput，实际: %v",
					err,
				)
			}

			if result != nil {
				t.Fatal("非法输入不应该返回项目结果")
			}
		})
	}
}
