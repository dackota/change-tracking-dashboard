package web

import "testing"

// TestBuildShell_CarriesPollHealthIntoHeader verifies R6/R11: buildShell
// threads the caller-supplied poll-health chip view straight into the
// shared header, so every page's header (built via buildShell) renders the
// same aggregate poll-status chip.
func TestBuildShell_CarriesPollHealthIntoHeader(t *testing.T) {
	t.Parallel()

	chip := statusChipView{
		Status:       statusError,
		LastPollText: "Last poll: 5 minutes ago",
		NextPollText: "Next poll: in 10 minutes",
		ErrorCount:   1,
		ErrorText:    "1 tracker failing",
	}

	got := buildShell("/", "Title", "Subtitle", "", chip)

	if got.Header.PollHealth != chip {
		t.Errorf("Header.PollHealth = %+v, want %+v", got.Header.PollHealth, chip)
	}
}

// TestBuildSidebarNav_MarksOnlyTheCurrentRegisteredRouteActive verifies R1:
// buildSidebarNav returns one entry per navRegistry slot, with a registered
// route's Href set and Active true only when it equals currentPath — never
// more than one entry active at a time.
func TestBuildSidebarNav_MarksOnlyTheCurrentRegisteredRouteActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		currentPath string
		wantActive  string // navKey expected Active; "" means none
	}{
		{"root path activates timeline", "/", "timeline"},
		{"trackers path activates trackers", "/trackers", "trackers"},
		{"unregistered path activates nothing", "/changes", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := buildSidebarNav(tc.currentPath)

			if len(items) != len(navRegistry) {
				t.Fatalf("len(items) = %d, want %d", len(items), len(navRegistry))
			}

			for _, item := range items {
				wantActive := tc.wantActive != "" && item.Key == tc.wantActive
				if item.Active != wantActive {
					t.Errorf("item %q: Active = %v, want %v", item.Key, item.Active, wantActive)
				}
			}
		})
	}
}

// TestBuildSidebarNav_OnlyRegisteredRoutesCarryAnHref verifies R1: a nav slot
// with no registered path (Changes, Repositories in this slice) renders with
// an empty Href — so the template can never emit a dead link for it — while
// Timeline and Trackers carry their real path.
func TestBuildSidebarNav_OnlyRegisteredRoutesCarryAnHref(t *testing.T) {
	t.Parallel()

	items := buildSidebarNav("/")

	want := map[string]string{
		"timeline":     "/",
		"changes":      "",
		"repositories": "",
		"trackers":     "/trackers",
	}

	got := make(map[string]string, len(items))
	for _, item := range items {
		got[item.Key] = item.Href
	}

	for key, wantHref := range want {
		if got[key] != wantHref {
			t.Errorf("item %q: Href = %q, want %q", key, got[key], wantHref)
		}
	}
}

// TestBuildSidebarNav_NeverMutatesSharedRegistryAcrossCalls asserts the
// immutability invariant called out for this slice: buildSidebarNav must
// build a fresh per-request slice every call, never mutate the shared
// navRegistry (or a previously returned slice). Calling it again for a
// different path must not retroactively change an earlier result still held
// by the caller.
func TestBuildSidebarNav_NeverMutatesSharedRegistryAcrossCalls(t *testing.T) {
	t.Parallel()

	first := buildSidebarNav("/")
	_ = buildSidebarNav("/trackers")
	_ = buildSidebarNav("/somewhere-else")

	for _, item := range first {
		wantActive := item.Key == "timeline"
		if item.Active != wantActive {
			t.Errorf("earlier result for %q mutated by later calls: Active = %v, want %v", item.Key, item.Active, wantActive)
		}
	}
}
