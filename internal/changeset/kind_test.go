package changeset_test

import (
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/changeset"
)

func TestClassifyKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filePath string
		want     changeset.Kind
	}{
		{
			name:     "Chart.yaml at repo root classifies as chart change",
			filePath: "Chart.yaml",
			want:     changeset.KindChart,
		},
		{
			name:     "Chart.yaml nested under tenant/env/region classifies as chart change",
			filePath: "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			want:     changeset.KindChart,
		},
		{
			name:     "values.yaml classifies as value change",
			filePath: "apps/tenant-zero/dev/us-west-2/values.yaml",
			want:     changeset.KindValue,
		},
		{
			name:     "arbitrary non-Chart.yaml file classifies as value change",
			filePath: "apps/tenant-zero/dev/us-west-2/some-other-file.yaml",
			want:     changeset.KindValue,
		},
		{
			name:     "file basename matching Chart.yaml in different directory still classifies as chart change",
			filePath: "infra/nested/deep/Chart.yaml",
			want:     changeset.KindChart,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := changeset.ClassifyKind(tc.filePath)

			if got != tc.want {
				t.Errorf("ClassifyKind(%q): got %q, want %q", tc.filePath, got, tc.want)
			}
		})
	}
}
