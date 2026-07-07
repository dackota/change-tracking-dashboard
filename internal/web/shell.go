// Package web (this file): the shared "shell" — the persistent left
// sidebar nav and the page header — every route's page handler builds its
// view data from (R6), so nav and header rendering never drift between
// routes. buildShell is the single entry point: it takes the current
// request path, a page's title/subtitle/header-actions, and the caller's
// already-computed poll-health chip view (R11), and returns the shellData
// every page's template embeds.
//
// R1: a sidebar nav slot is a real, navigable link only once its route is
// registered (has a non-empty path in navRegistry); an unregistered slot
// renders inert — no href, no click handler — so it can never produce a
// dead link ahead of the slice that adds its route. The slot whose
// registered path equals the current request path is marked Active.
//
// R11: every page's header (built via buildShell) renders the same
// aggregate poll-status chip, computed by statusChip (pollhealth.go) from
// the live pollstatus snapshot the calling handler holds a reference to.
package web

import "html/template"

// navKey is the stable identifier for one persistent sidebar entry,
// rendered as the data-nav attribute every page's template keys off of.
type navKey string

const (
	navTimeline     navKey = "timeline"
	navChanges      navKey = "changes"
	navRepositories navKey = "repositories"
	navTrackers     navKey = "trackers"
)

// navEntry is one fixed sidebar slot: its stable key, display label, and
// the path it routes to once that route exists. An empty path means "not
// yet a route" — buildSidebarNav renders it inert rather than a link that
// would 404, until the slice that adds the route lands.
type navEntry struct {
	key   navKey
	label string
	path  string // "" until the route exists
}

// navRegistry is the fixed v1 sidebar, in display order. Only Timeline and
// Trackers have a registered path in this slice; Changes and Repositories
// are reserved slots for their own downstream slices. It is read-only at
// runtime — buildSidebarNav never mutates it, only copies out of it.
var navRegistry = []navEntry{
	{navTimeline, "Timeline", "/"},
	{navChanges, "Changes", ""},
	{navRepositories, "Repositories", ""},
	{navTrackers, "Trackers", "/trackers"},
}

// sidebarNavItem is one rendered sidebar entry (R1). Href is "" for a nav
// slot with no registered route yet — the template renders that case as a
// plain, non-interactive element, never a dead link.
type sidebarNavItem struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

// headerView is the page header rendered above every route's body (R6): a
// title, a short descriptive subtitle, any page-specific header actions
// (e.g. the timeline's Reset zoom button) as pre-rendered, trusted markup,
// and the aggregate poll-status chip (R11) shared by every page's header.
// Actions is always a compile-time constant supplied by the calling
// handler — never built from request or stored data — so bypassing
// html/template's usual auto-escaping here does not open an injection path.
type headerView struct {
	Title      string
	Subtitle   string
	Actions    template.HTML
	PollHealth statusChipView
}

// shellData is the sidebar+header chrome every page handler's view data
// embeds (R6), so nav and header rendering stay identical across routes.
type shellData struct {
	SidebarNav []sidebarNavItem
	Header     headerView
}

// buildShell assembles the shared shell (sidebar nav + header) for a
// request to currentPath. Every page handler calls this to build its own
// view data so nav and header stay consistent across routes (R6). pollHealth
// is the caller's already-computed aggregate poll-status chip (R11) — built
// by statusChip from the live pollstatus snapshot — so buildShell itself
// stays a pure function with no I/O.
func buildShell(currentPath, title, subtitle string, actions template.HTML, pollHealth statusChipView) shellData {
	return shellData{
		SidebarNav: buildSidebarNav(currentPath),
		Header:     headerView{Title: title, Subtitle: subtitle, Actions: actions, PollHealth: pollHealth},
	}
}

// buildSidebarNav builds a fresh per-request sidebar nav slice for
// currentPath (R1). It never mutates the shared navRegistry — each call
// allocates its own slice, so no two requests (or two calls for different
// paths within the same request) can observe or corrupt each other's
// Active/Href values.
func buildSidebarNav(currentPath string) []sidebarNavItem {
	items := make([]sidebarNavItem, 0, len(navRegistry))
	for _, e := range navRegistry {
		items = append(items, sidebarNavItem{
			Key:    string(e.key),
			Label:  e.label,
			Href:   e.path,
			Active: e.path != "" && e.path == currentPath,
		})
	}
	return items
}
