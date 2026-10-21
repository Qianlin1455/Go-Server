package service

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{"todo到doing", "todo", "doing", true},
		{"doing到todo", "doing", "todo", true},
		{"doing到done", "doing", "done", true},
		{"todo直接到done", "todo", "done", false},
		{"done重新开始", "done", "doing", false},
		{"重复状态", "todo", "todo", false},
		{"未知目标", "todo", "cancelled", false},
		{"未知来源", "unknown", "doing", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanTransition(tc.from, tc.to)
			if got != tc.want {
				t.Errorf(
					"CanTransition(%q, %q) = %v, want %v",
					tc.from, tc.to, got, tc.want,
				)
			}
		})
	}
}
