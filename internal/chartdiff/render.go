package chartdiff

import (
	"github.com/dackota/change-tracking-dashboard/internal/chartrender"
	"github.com/dackota/change-tracking-dashboard/internal/manifestdiff"
)

// Renderer is the chart-render seam Engine.Diff depends on. helmRenderer
// (below) is the production adapter over chartrender.Render; tests inject a
// fake to exercise timeout, concurrency, and cache behavior deterministically
// without invoking the real Helm SDK.
type Renderer interface {
	// Render renders chartDir with values merged over the chart's own
	// values.yaml (nil renders with the chart's committed values only). On
	// failure it returns a *chartrender.Failure classifying why.
	Render(chartDir string, values map[string]interface{}) (*chartrender.Result, error)
}

// helmRenderer is the default Renderer, delegating directly to
// chartrender.Render — the sole production entry point into the Helm SDK.
type helmRenderer struct{}

func (helmRenderer) Render(chartDir string, values map[string]interface{}) (*chartrender.Result, error) {
	return chartrender.Render(chartDir, values)
}

// toManifestdiffManifests maps a chartrender.Result's manifest set onto
// manifestdiff's independent (identically shaped) Manifest type, so
// manifestdiff never needs to import chartrender (ADR 0002: the heavy Helm
// SDK dependency stays contained to chartrender).
func toManifestdiffManifests(result *chartrender.Result) []manifestdiff.Manifest {
	if result == nil {
		return nil
	}
	out := make([]manifestdiff.Manifest, len(result.Manifests))
	for i, m := range result.Manifests {
		out[i] = manifestdiff.Manifest{
			Kind:      m.Kind,
			Namespace: m.Namespace,
			Name:      m.Name,
			YAML:      m.YAML,
		}
	}
	return out
}
