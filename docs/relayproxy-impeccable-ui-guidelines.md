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
