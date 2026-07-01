package changeset

import "path/filepath"

// Kind classifies a Change by the role of the file it came from.
type Kind string

const (
	// KindChart is a Change sourced from a Chart.yaml file.
	KindChart Kind = "chart"
	// KindValue is a Change sourced from any file other than Chart.yaml
	// (e.g. values.yaml).
	KindValue Kind = "value"
)

// ClassifyKind classifies a Change by the basename of its source file path.
// Chart.yaml yields KindChart; every other basename (e.g. values.yaml)
// yields KindValue.
func ClassifyKind(filePath string) Kind {
	if filepath.Base(filePath) == "Chart.yaml" {
		return KindChart
	}
	return KindValue
}
