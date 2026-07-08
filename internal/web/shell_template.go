package web

// shellStyles is the CSS for the app-frame layout, the persistent sidebar
// (including a nav item's active state), and the page header — shared by
// every page (R6) so the shell looks and behaves identically across routes.
// Every page's own <style> block includes this exact text verbatim; only
// the page-specific rules below it differ.
const shellStyles = `
    :root {
      /* option-C palette tokens — the single system for the whole page. */
      --oc-canvas:#f1f5f9; --oc-panel:#fff; --oc-ink:#0f172a; --oc-muted:#64748b;
      --oc-line:#e2e8f0; --oc-line-soft:#f1f5f9; --oc-accent:#2563eb;
      --oc-sidebar:#0f172a; --oc-sidebar-ink:#cbd5e1;
      --oc-danger:#dc3545; --oc-success:#198754;
      --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body { margin: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: var(--oc-canvas); color: var(--oc-ink); }

    /* App shell: persistent sidebar + main content column */
    .app { display: flex; min-height: 100vh; align-items: stretch; }
    .sidebar { flex: 0 0 210px; background: var(--oc-sidebar); color: var(--oc-sidebar-ink); padding: 1.1rem 0.9rem; display: flex; flex-direction: column; gap: 0.3rem; }
    .sidebar-brand { display: flex; align-items: center; gap: 0.55rem; padding: 0.2rem 0.4rem 1rem; font-weight: 650; color: #fff; font-size: 0.95rem; }
    .sidebar-logo { width: 24px; height: 24px; border-radius: 7px; background: linear-gradient(135deg, #2563eb, #06b6d4); display: grid; place-items: center; font-size: 0.85rem; }
    .sidebar-nav { display: flex; flex-direction: column; gap: 0.3rem; }
    .nav-item { font-size: 0.84rem; color: var(--oc-sidebar-ink); padding: 0.5rem 0.6rem; border-radius: 8px; display: flex; align-items: center; gap: 0.55rem; text-decoration: none; }
    .nav-item-active { background: rgba(37, 99, 235, 0.18); color: #fff; }

    .main { flex: 1 1 auto; min-width: 0; padding: 1.4rem 1.8rem 4rem; }
    .page-header { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 1.2rem; }
    .page-header h1 { margin: 0; font-size: 1.25rem; font-weight: 700; letter-spacing: -0.01em; }
    .page-subtitle { color: var(--oc-muted); font-size: 0.85rem; margin: 0; }
    .page-header .spacer { flex: 1; }
    .page-header .btn { font-size: 0.8rem; background: var(--oc-panel); border: 1px solid var(--oc-line); border-radius: 8px; padding: 0.4rem 0.75rem; cursor: pointer; color: var(--oc-ink); }

    /* Aggregate poll-status chip (R11), rendered in every page's header. */
    .poll-chip { display: flex; align-items: center; gap: 0.45rem; font-size: 0.78rem; padding: 0.35rem 0.7rem; border-radius: 999px; background: var(--oc-line-soft); color: var(--oc-muted); border: 1px solid var(--oc-line); white-space: nowrap; }
    .poll-chip-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--oc-muted); flex: 0 0 auto; }
    .poll-chip-ok .poll-chip-dot { background: var(--oc-success); }
    .poll-chip-error { background: rgba(220, 53, 69, 0.08); border-color: rgba(220, 53, 69, 0.35); color: var(--oc-danger); }
    .poll-chip-error .poll-chip-dot { background: var(--oc-danger); }

    /* Wide-table containment (R18): any page's data table opts into this
       wrapper so an unavoidably wide table scrolls locally, never forcing
       the whole page to scroll horizontally. */
    .table-scroll { width: 100%; overflow-x: auto; -webkit-overflow-scrolling: touch; }

    /* Responsive shell (R15-R18): below ~860px the persistent sidebar column
       no longer fits alongside real content, so it collapses into a
       full-width horizontal top bar and the app stacks vertically instead.
       The poll-chip's wrap override lives in this same breakpoint (not the
       600px one below) because the chip is nowrap by default and its text
       — worst case the error state's "· N tracker(s) failing" suffix — is
       long enough to overflow horizontally in the ~620-720px tablet band
       even though the sidebar has already collapsed and nothing else in the
       layout overflows there. Below ~600px the header row
       (title/subtitle/poll-chip/actions) also wraps rather than overflowing,
       and the main column's side padding shrinks to give narrow viewports
       more usable width. */
    @media (max-width: 860px) {
      .app { flex-direction: column; }
      .sidebar { flex: 0 0 auto; width: 100%; flex-direction: row; flex-wrap: wrap; align-items: center; padding: 0.6rem 0.9rem; gap: 0.5rem 0.9rem; }
      .sidebar-brand { padding: 0; margin-right: auto; }
      .sidebar-nav { flex-direction: row; flex-wrap: wrap; gap: 0.3rem 0.5rem; }
      .poll-chip { white-space: normal; }
    }
    @media (max-width: 600px) {
      body { overflow-x: hidden; }
      .main { padding: 1rem 1rem 3rem; }
      .page-header { flex-wrap: wrap; }
    }
`

// sidebarTemplate defines the "sidebar" named template shared by every page
// (R1, R6). A nav item with a non-empty Href renders as a real <a> link,
// marked aria-current when Active; an item with no Href (its route isn't
// registered yet) renders as a plain, non-interactive <div> — never a dead
// link.
const sidebarTemplate = `{{define "sidebar"}}<aside class="sidebar">
      <div class="sidebar-brand"><span class="sidebar-logo">◆</span> ChangeTrack</div>
      <nav class="sidebar-nav" aria-label="Primary">
        {{range .SidebarNav}}{{if .Href}}<a class="nav-item{{if .Active}} nav-item-active{{end}}" data-nav="{{.Key}}" href="{{.Href}}"{{if .Active}} aria-current="page"{{end}}>{{.Label}}</a>
        {{else}}<div class="nav-item" data-nav="{{.Key}}">{{.Label}}</div>
        {{end}}{{end}}
      </nav>
    </aside>{{end}}`

// headerTemplate defines the "header" named template shared by every page
// (R6): title, subtitle, the aggregate poll-status chip (R11, plus R9's
// per-engine extract-failure counts — acceptance criterion 9), and any
// page-specific header actions (pre-rendered trusted HTML, e.g. the
// timeline's Reset zoom button). The chip's Status field only ever takes one
// of the fixed values statusUnknown/statusOK/statusError (never
// request/stored data), so interpolating it straight into the class
// attribute carries no injection risk;
// LastPollText/NextPollText/ErrorText/ExtractFailureText/
// PlanDiffOutcomeText go through html/template's default auto-escaping like
// any other field.
const headerTemplate = `{{define "header"}}<div class="page-header">
        <h1>{{.Header.Title}}</h1>
        <p class="page-subtitle">{{.Header.Subtitle}}</p>
        <span class="spacer"></span>
        <div class="poll-chip poll-chip-{{.Header.PollHealth.Status}}" data-poll-status="{{.Header.PollHealth.Status}}">
          <span class="poll-chip-dot"></span>
          <span class="poll-chip-text">{{.Header.PollHealth.LastPollText}} · {{.Header.PollHealth.NextPollText}}{{if .Header.PollHealth.ErrorText}} · {{.Header.PollHealth.ErrorText}}{{end}}{{if .Header.PollHealth.ExtractFailureText}} · {{.Header.PollHealth.ExtractFailureText}}{{end}}{{if .Header.PollHealth.PlanDiffOutcomeText}} · plan-diff: {{.Header.PollHealth.PlanDiffOutcomeText}}{{end}}</span>
        </div>
        {{.Header.Actions}}
      </div>{{end}}`

// shellTemplates is the combined text every page handler's template.Parse
// call includes so "sidebar" and "header" are available as named templates
// (R6) — one authored definition, reused everywhere.
const shellTemplates = sidebarTemplate + headerTemplate
