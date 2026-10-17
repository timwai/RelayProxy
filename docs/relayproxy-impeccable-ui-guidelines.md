# RelayProxy UI quality guidelines (Impeccable-inspired)

Updated: 2026-10-17

This guide applies the design reasoning from [Impeccable](https://github.com/pbakaus/impeccable) to RelayProxy's existing product identity. It does not replace the React components or change the network feature model.

## Product-specific principles

1. **Operational clarity first.** Agent and Server are tools for routing, connecting, authorizing and transferring. Lead with current state, then a single primary action; hide secondary configuration behind an explicit entry point.
2. **Preserve deep links.** Moving an Agent feature into Settings does not invalidate its `?page=` link. Keep Server hash routes and permissions checks intact.
3. **No duplicated headings.** A top bar identifies the active page. Page bodies can contain a visually hidden semantic `h1`, short contextual copy, then a compact action row.
4. **Do not replace all lists with cards.** Operational tables, connection streams, logs and file lists need stable columns, useful density, truncation and keyboard navigation.
5. **State is not decoration.** Connected, offline, negotiating, waiting for approval, error and transferring must have consistent words and accessible indicators. Color is secondary to text.
6. **File interactions follow explorer conventions.** Single click selects, double click opens folders, Ctrl/Cmd or Shift enables multiselect, checkbox selection remains independent, Enter opens a selected folder. Selection actions float rather than shifting files.
7. **Dialog interactions work from the keyboard.** Name the dialog, focus the first usable control, cycle Tab within it, support Escape and return focus to the invoking control. Destructive actions keep explicit confirmation.
8. **Readable density.** Agent base body: 14px; supporting text: 12px; small category labels: 11px. Server already has an existing 14px content type scale; avoid globally applying a second one.
9. **Respect platform differences.** Share semantic tokens between Agent React, Server React and the browser extension before considering component extraction; Android native UI follows platform navigation and touch rules.
10. **Prefer subtle motion.** Reduced motion must not lose meaning; do not use animated layout changes as the only indication of activity.

## Recommended iterative loop

- `distill`: remove duplicate UI and reduce unnecessary choices.
- `layout`: assign clear reading order, spacing and responsive behavior.
- `audit`: verify keyboard, contrast, focus, overflow and dark mode.
- `harden`: test loading, empty, error, offline, insufficient permission and cancellation cases.
- `polish`: check consistency and final shipping readiness.

## Acceptance checklist

- Agent: side menu and Settings sub-navigation retain all routes and dirty-form confirmation.
- Agent: file explorer single/double-click, authorized shares only, progress and transfer removal.
- Server: no repeated large page heading under the breadcrumb; page actions remain reachable at 320px.
- Both: dialogs trap focus and restore it on close, including Escape, clickable backdrop and action buttons.
- Both: keyboard focus rings remain visible in light and dark themes.
- Test at narrow desktop and mobile widths, with 200% text zoom and long localized labels.
- Run `npm test` and `npm run build` in both React frontend directories before merging.

## Change management

Roll UI changes out incrementally; avoid large UI rewrites that touch networking logic. A passing source-level test is not a substitute for manual browser and Wails interaction acceptance.

## Iteration 2: information density (2026-10-17)

- Remote file browser takes the remaining content height; main chrome and tab controls do not reserve an arbitrary 255px deduction.
- The transfer dock is initially collapsed and expands when active transfer count becomes nonzero. Users can collapse it again manually.
- Agent monitoring renders the first 100 matching connections, supports incremental loading, and keeps table headers visible inside a keyboard-scrollable region. Filtering resets the visible limit.
- Server data tables expose `scope="col"`, keyboard scrolling and sticky column headers. Large data sets receive a bounded viewport while horizontally wide diagnostics remain scrollable.
- Source assertions cover these invariants. Browser / Wails acceptance should additionally check 768px, 1280px, 1920px, dark mode, high DPI, vertical resize, ongoing transfers and keyboard scrolling.
- Related risk: Windows UI CI can fail in unrelated WinDivert firewall live integration tests if Windows Defender Firewall rejects test rule creation; do not treat that as a passing UI acceptance nor change firewall protections during a UI-only review.

## Iteration 3: themes, narrow windows and keyboard accessibility

- Give compact Agent and Server sidebar destinations explicit accessible names, hover titles and `aria-current="page"` markers, especially where labels are hidden and only icons remain.
- Keep Server theme selection available from the mobile drawer with a clearly named group and pressed state. The top-bar picker stays on desktop.
- Remove closed mobile navigation from the tab order using responsive CSS visibility, and return focus after Escape or navigation.
- Set native input `color-scheme` for light/dark modes and use `100dvh` where supported without dropping the existing `100vh` fallback in Wails.
- Keyboard file browsing: Space selects a focused file row; Enter opens a folder or shows file details. Checkboxes and per-file menus remain separate controls. Focused file rows have visible outlines.
- Reuse one Agent `useDialogFocus` hook in both the main app and file-operation dialogs; ensure move, rename, create and delete confirmations have semantic labels and focus restoration.
- Responsive and accessibility source tests live next to the two React frontends.
- UI-only PR CI does not prove dark-mode contrast, VoiceOver/NVDA operation or a Wails window has been rendered; acceptance should include visual checks and real keyboard traversal before merge.

## Iteration 4: page actions without wasted full-width rows

- Agent page-level actions mount in the existing 44px title bar on windows at least 1100px wide. Dirty/restart indicators remain present; action controls remain clickable in the Wails drag region.
- Below that threshold, controls stay in a compact, left-aligned page toolbar rather than a right-aligned band with unused space.
- Server page-level actions mount into the existing title bar on wide consoles (at least 1380px). Narrow layouts keep short context text and actions together from the left, wrapping only if necessary.
- The Browser Sync management header uses a screen-reader heading with compact inline context/refresh instead of duplicating the large page title already in the top bar.
- File sharing tabs and actions share the natural flow even under 680px rather than forcing a separate right-justified action row.
- Validate at 360/768/1024/1280/1440px, including long device names, action toolbar overflow, selected exit warnings, browser zoom 200%, and hover/keyboard behavior.

## Iteration 5: RDP and message configuration under Settings

- Agent `SettingsNavigation` now owns two additional categories: `rdp-settings` (local controller switch / runtime and Server capability status / save and restart) and `message-settings` (popup auto-dismiss duration / Server-owned rule status / preview variants and save).
- The operational `rdp` page retains authorized-device listing, connect/disconnect, copy local endpoint and active-path diagnostics. A disabled or unapproved controller continues to show a status explanation without in-page configuration controls.
- The operational `messages` page retains message history, type filters, search, code copy and refresh/clear; popup policy and previews are no longer mixed with history.
- No config keys or mutation API calls changed: `rdp.enabled` still saves under `rdp`, and `verificationPopupTimeoutSec` still saves under `gui`.
- Settings navigation is a single horizontally scrollable row with tooltips to prevent new categories from adding vertical page-header rows. Existing page IDs and operational routes stay supported.
- Regression test `agent/gui/frontend/src/settings-placement.test.js` enforces route presence, operational/configuration separation and preservation of save and preview behavior.

## Iteration 6: unified native select sizing

- All **single-choice** HTML `select` controls in Agent (Wails / Web UI) and Server Web use a consistent `44px` minimum target, `48px` below `640px`, `14px` text, clearly visible caret and 10px radius.
- The universal selector `select:not([multiple]):not([size])` intentionally excludes multi-select listboxes, preserving native list behavior.
- The chevron switches from dark to light stroke with `html[data-theme="dark"]`; hover, focus-visible, disabled and option colors follow existing design tokens.
- Covered screens include Agent routing / DNS selectors, settings, file sharing permissions, file-browser sorting, monitoring; Server filters, configuration groups, RDP/security dialogs and Browser Sync approval.
- The dropdown menu itself can be drawn by the OS / embedded WebView; the visual skin and height of OS-owned expanded options are not controllable via CSS. Manual acceptance should exercise macOS and Windows native Wails builds plus desktop and mobile browsers.
- Regression tests: `agent/gui/frontend/src/select-style.test.js` and `server/web/frontend/src/select-style.test.js`. No backing config schemas or select `onChange` handlers changed.
