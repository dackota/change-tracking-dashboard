package changeset_test

import (
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
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
		{
			name:     "file merely containing .tf as a substring (not the extension) classifies as value change",
			filePath: "envs/prod/notes.tf.md",
			want:     changeset.KindValue,
		},
		{
			name:     "versions.tf classifies as provider (required_providers + required_version live here)",
			filePath: "versions.tf",
			want:     changeset.KindProvider,
		},
		{
			name:     "providers.tf classifies as provider",
			filePath: "providers.tf",
			want:     changeset.KindProvider,
		},
		{
			name:     ".terraform.lock.hcl classifies as provider (lockfile pins)",
			filePath: ".terraform.lock.hcl",
			want:     changeset.KindProvider,
		},
		{
			name:     "nested .terraform.lock.hcl still classifies as provider",
			filePath: "terraform/.terraform.lock.hcl",
			want:     changeset.KindProvider,
		},
		{
			name:     "modules.tf classifies as module",
			filePath: "modules.tf",
			want:     changeset.KindModule,
		},
		{
			name:     "variables.tf classifies as variable",
			filePath: "variables.tf",
			want:     changeset.KindVariable,
		},
		{
			name:     "an arbitrary resource-defining .tf file classifies as resource (the default Terraform Kind)",
			filePath: "terraform/oci-containerengine-nodepool.tf",
			want:     changeset.KindResource,
		},
		{
			name:     "node_pool.tf classifies as resource",
			filePath: "node_pool.tf",
			want:     changeset.KindResource,
		},
		{
			name:     ".tofu file follows the same Terraform classification as .tf",
			filePath: "main.tofu",
			want:     changeset.KindResource,
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

func TestKind_IsTerraform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind changeset.Kind
		want bool
	}{
		{changeset.KindProvider, true},
		{changeset.KindModule, true},
		{changeset.KindResource, true},
		{changeset.KindVariable, true},
		{changeset.KindChart, false},
		{changeset.KindValue, false},
	}

	for _, tc := range tests {
		if got := tc.kind.IsTerraform(); got != tc.want {
			t.Errorf("Kind(%q).IsTerraform() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
