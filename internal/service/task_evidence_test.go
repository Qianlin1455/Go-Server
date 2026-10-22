package service

import (
	"errors"
	"strings"
	"testing"

	"GoServer/internal/model"
)

func strPtr(s string) *string {
	return &s
}

func TestValidateEvidenceInput(t *testing.T) {
	cases := []struct {
		name  string
		input CreateEvidenceInput
		want  error
	}{
		{
			name: "合法实验文字",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeExperimentText,
				Text:   strPtr("CAS并发测试通过"),
				Status: model.EvidenceStatusSubmitted,
			},
		},
		{
			name: "空草稿合法",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeCommitSHA,
				Status: model.EvidenceStatusDraft,
			},
		},
		{
			name: "正式提交内容为空",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeTestOutput,
				Status: model.EvidenceStatusSubmitted,
			},
			want: ErrInvalidEvidenceContent,
		},
		{
			name: "非法状态",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeTestOutput,
				Status: "unknown",
			},
			want: ErrInvalidEvidenceStatus,
		},
		{
			name: "非法证据类型",
			input: CreateEvidenceInput{
				Type:   "unknown",
				Status: model.EvidenceStatusDraft,
			},
			want: ErrInvalidEvidenceType,
		},
		{
			name: "超长文本",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeExperimentText,
				Text:   strPtr(strings.Repeat("你", 10001)),
				Status: model.EvidenceStatusSubmitted,
			},
			want: ErrInvalidEvidenceContent,
		},
		{
			name: "合法40位SHA",
			input: CreateEvidenceInput{
				Type:      model.EvidenceTypeCommitSHA,
				CommitSHA: strPtr(strings.Repeat("a", 40)),
				Status:    model.EvidenceStatusSubmitted,
			},
		},
		{
			name: "非法SHA字符",
			input: CreateEvidenceInput{
				Type:      model.EvidenceTypeCommitSHA,
				CommitSHA: strPtr(strings.Repeat("a", 39) + "z"),
				Status:    model.EvidenceStatusSubmitted,
			},
			want: ErrInvalidEvidenceContent,
		},
		{
			name: "SHA类型错误携带Text",
			input: CreateEvidenceInput{
				Type:   model.EvidenceTypeCommitSHA,
				Text:   strPtr("错误字段"),
				Status: model.EvidenceStatusDraft,
			},
			want: ErrInvalidEvidenceContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEvidenceInput(tc.input)

			if !errors.Is(err, tc.want) {
				t.Fatalf(
					"want=%v, got=%v",
					tc.want, err,
				)
			}
		})
	}
}
