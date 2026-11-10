package model

import "testing"

func TestCanTransitionImportJob(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{
			name: "queued to running",
			from: ImportJobQueued,
			to:   ImportJobRunning,
			want: true,
		},
		{
			name: "running to succeeded",
			from: ImportJobRunning,
			to:   ImportJobSucceeded,
			want: true,
		},
		{
			name: "running to failed",
			from: ImportJobRunning,
			to:   ImportJobFailed,
			want: true,
		},
		{
			name: "running to queued for retry",
			from: ImportJobRunning,
			to:   ImportJobQueued,
			want: true,
		},
		{
			name: "queued cannot skip to succeeded",
			from: ImportJobQueued,
			to:   ImportJobSucceeded,
			want: false,
		},
		{
			name: "queued cannot skip to failed",
			from: ImportJobQueued,
			to:   ImportJobFailed,
			want: false,
		},
		{
			name: "succeeded is terminal",
			from: ImportJobSucceeded,
			to:   ImportJobRunning,
			want: false,
		},
		{
			name: "failed is terminal",
			from: ImportJobFailed,
			to:   ImportJobQueued,
			want: false,
		},
		{
			name: "same state is forbidden",
			from: ImportJobQueued,
			to:   ImportJobQueued,
			want: false,
		},
		{
			name: "unknown source state",
			from: "unknown",
			to:   ImportJobRunning,
			want: false,
		},
		{
			name: "unknown target state",
			from: ImportJobRunning,
			to:   "unknown",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanTransitionImportJob(tt.from, tt.to)

			if got != tt.want {
				t.Errorf(
					"CanTransitionImportJob(%q, %q) = %v, want %v",
					tt.from,
					tt.to,
					got,
					tt.want,
				)
			}
		})
	}
}
