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

## Iteration 7: operational pages vs frequently edited configuration vs low-frequency settings

Information architecture principle: **daily operations** are available from the left sidebar; **Network Configuration** contains traffic behavior and network policies, which users edit repeatedly; **Settings** contains equipment identity, operating-system preferences and presentation behavior.

| Area | Navigation | Owns | Does not own |
| --- | --- | --- | --- |
| 工作台 | 运行概览、出口选择 | Connection state, choosing an already configured exit, tests, speed and path | Creating proxy definitions |
| 协作 | 文件共享、远程桌面、消息 | Working with authorized shares/devices, active RDP, messages and history | RDP startup switch, popup preferences |
| 观察 | 实时监控、诊断与日志 | Diagnostics and current traffic | Startup/config values |
| 网络配置 | 分流规则 (default), DNS 与防泄漏, 代理与自定义出口, 连接策略, 出口共享 | Routing rules/subscriptions, DNS policies, local proxy listeners and custom exits, QUIC/P2P preferences and exit-sharing ACL | Desktop appearance and app lifecycle |
| 设置 | 常规设置, 设备与身份, 远程桌面偏好, 消息与弹窗 | Appearance, autostart, maintenance, local device identity, RDP capability enablement and popup duration | Frequently edited routing rules and DNS |

- **One visible Network Configuration entry**, defaulting to **分流规则**, prevents flooding the sidebar with individual low-level knobs. Its five subcategories use the same compact horizontally scrollable navigation component as Settings, which now has only four subcategories.
- Existing bookmark `?page=connection`, `?page=proxy`, `?page=exitshare`, `?page=dns`, `?page=routing`, `?page=devices`, `?page=rdp-settings` and `?page=message-settings` remain valid; the backend configuration schema and save payloads are unchanged.
- Selected configuration subpages keep the Network Configuration sidebar entry active; Settings remains highlighted only for low-frequency settings subpages.
- The operational Exit Picker now lists both authorized Server exits and configured local exits, including disabled status. The `CustomExitManager` editing UI moves into **代理与自定义出口**; its network test and save APIs stay the same.
- While a proxy draft is dirty, saving an independent custom exit is blocked with a descriptive message, avoiding silent loss of the draft from `refreshConfig`.
- RDP and message pages remain operational only; the controls moved in the previous iteration stay in Settings.
- Updated and new regression tests: `ui-responsive.test.js`, `settings-placement.test.js`, `page-head-layout.test.js`, `navigation-architecture.test.js`.
- Acceptance: verify navigation from each deep link, keyboard focus in horizontal category navigation, unsaved-state discard prompts and local exit selection plus diagnostics at 820px/1100px widths.

## Iteration 8: Device Center, capability shortcuts and default exit placement

- Rename the Agent sidebar's operational `exits` route from **出口选择** to **设备中心** without removing old `?page=exits` bookmarks.
- Merge authenticated Agent proxy exit inventory, RDP authorized targets, and **only live directory-ACL-verified file hosts** by device ID. Never infer file access from `files.host` capability or the Server peer list alone. Local SOCKS5/HTTP proxy definitions remain in Network Configuration, separate from physical remote device cards.
- Each remote device card shows only the capabilities discovered for that device: `共享文件` (verified `shares`), `远程桌面` (authorized RDP target and local controller enabled), `测速` (proxy exit), and `设备详情` (source-specific status). Actions disable when the session/device is offline; generic card selection never modifies the proxy route.
- The file shortcut mounts the existing `FileSharingPage` with an initial device ID. That page re-fetches and validates the directory ACL before showing content, opens the sole authorized share automatically, or shows the list of authorized shares when several are present. No new file transfer implementation; preserves directory cache, download/upload flow, prewarming and permissions.
- The RDP shortcut reuses the existing `goConnectRDP` path with an identity-bound authorized target, then opens the RDP session page. The speed shortcut reuses the existing per-exit speed modal; its results and the detail view must never falsely show the current default exit's global path as another device's measurement.
- `网络配置 → 分流规则 → 全局路由策略` now owns `默认代理出口` using the **existing** `defaultExitId` proxy save key, independent of `routing.default_action`. Rule-/subscription-specific `exit_id` continues to override the default. Disconnected, deleted or revoked exit references must not cause silent fallback to local DIRECT.
- Refresh verified file hosts every five seconds while Device Center is displayed and fail closed on errors/revocation; leave the old operational `exits` URI valid.
- Tests: `deviceCenter.test.js`, `device-center-ui.test.js`, adjusted `navigation-architecture.test.js`. Manual acceptance: proxy-only, RDP-only and files-only hosts; mixed capabilities; offline/revoked ACL; multi-share navigation; double-click file explorer; small Wails window and 200% zoom.

## Iteration 9: overview scope and device-based diagnostics

- **Overview** only retains global connection/approval state, currently configured or reported exit, four live aggregate traffic metrics, and the latest three connections. Keep entry points to Device Center, routing policy and real-time monitoring. Its former half-width "proxy path" card, standalone transparent proxy/exit-sharing/local-proxy cards and duplicated identity card are removed.
- **Device Center** now owns an expandable **当前代理出口 · 链路诊断** section with path classification, current-path Direct/P2P/Relay, *local* UPnP gateway state and native UDP resources. The section explicitly describes the **active proxy session**, never all remote devices.
- **Device Details** continue to show the specific device's authorized proxy/RDP/file capabilities and verified public candidate endpoints. Only when `status.connected && status.selectedExit === selected.id` may the UI display that device's current *actual* proxy path; otherwise the candidate endpoints remain capabilities, not measurements. A dedicated per-device speed test uses its exact exit ID.
- RDP TCP/UDP route details already exist under **远程桌面 → 当前 RDP 会话**; local Windows capture controls under **网络配置 → 代理与自定义出口**; UPnP configuration under **连接策略**; identity and approval under **设置 → 设备与身份**. Avoid duplicating these blocks in Overview.
- Added `agent/gui/frontend/src/overview-scope.test.js` to guard overview density and strict route source labeling. No Go API, config schema or network behavior changed.

## Iteration 10: content-first routing and file-sharing views

- **Routing Rules** opens with **规则列表** immediately after the shared configuration tabs and page actions, followed by **规则订阅**. The formerly dominant **全局路由策略** form is now a collapsed disclosure underneath the lists, with a readable summary of mode, default proxy exit and unmatched-rule action. All save fields, subscriptions, drag ordering, and rule-edit operations remain unchanged. Native `details` preserves keyboard access without a new state machine.
- **文件共享 → 我的共享** opens on the actual **共享文件夹** list, with an always-visible lightweight directory count/active count and sharing service status. The three metric blocks, main sharing service toggle and file-only UPnP TCP/QUIC mappings are consolidated into a discoverable **共享服务与直连设置** disclosure **below** the folder list. The independent permission editor remains available on each folder.
- **文件共享 → 其他设备** keeps the left column strictly for the **selected device's authorized directory tree** (share roots and lazily expanded folder descendants); no peer list or per-peer headers in the sidebar. A new accessible **切换共享设备** selector in the explorer header lists only peers verified by `onlyVerifiedFilePeers`. Mobile collapses the tree into the existing folder drawer while keeping the top device selector visible; dedicated/focus windows use the same component.
- On a fresh visit without an explicit file deep link, auto-select only the first *ACL-verified online* device to show its folder roots immediately. Remember that the initial automatic choice already happened; later revoked/offline devices clear selection without silently switching to a different device. Explicit Device Center and standalone `fileDevice/fileShare/filePath` targets retain priority and fail closed if unauthorized.
- The browser does not copy any stale, unauthenticated shares into the tree: `visibleShares` is intersected with the current verified peer ACL. The backend list-files and upload/mutation checks remain intact.
- Regression coverage: `agent/gui/frontend/src/content-first-navigation.test.js`, alongside existing V4 file-browser and visibility tests. Native Windows/macOS Wails 820px viewport and standalone file explorer should be visually accepted prior to merging.

## Iteration 11: device-scoped link evidence belongs to per-device speed tests

**Implemented:** Remove the aggregate **当前代理出口 · 链路诊断** card from Device Center, including its duplicated global status fields. Each device's **测速** shortcut now opens the wider **设备双向测速与链路信息** modal, which shows:
- The target device ID and its specific, currently authorized proxy exit inventory entry.
- **Public Direct:** an exit-specific advertised/verified candidate indication, **not** an assertion that public direct is active. It becomes **本次测速使用** only when the exact exit's speed-test response identifies that transport.
- **P2P QUIC and Relay:** independently labelled according to the exact same speed-test response, not guessed from global `status.directPath` / `status.p2pPath`. Upload/download may use different paths; show both.
- Measured throughput, per-leg QUIC RTT, jitter, packet loss, GSO, congestion control and relevant UDP buffers from the existing speed result. The response must carry the requested `exitId` before being attributed to that device.
- Existing **UPnP gateway**, **Native UDP associations** and Server control state under a distinct, collapsed **本机网络环境** section. These are local Agent diagnostics, never remote-peer proof. Current RDP TCP/UDP paths appear only for an exact-match active RDP target.
- Retain per-device BBR five-round calibration without automatically enabling Brutal, and do not change Agent/Server protocol or routing configuration.
- See `deviceSpeedLink.js` and `deviceSpeedLink.test.js` for the fail-closed provenance logic; `overview-scope.test.js` now enforces the per-device placement.

**Proposal only — next file-sharing navigation iteration (not implemented in this batch):**
- Remove the three top-level file-sharing tabs. Keep **文件共享** in the Agent navigation as the direct entry to **我的共享** (file-share directories list, permissions and lower advanced settings). Do not add separate permanently visible main-sidebar entries.
- Make remote browsing device-oriented: **设备中心 → [设备] → 共享文件** opens that device in File Browser V4. The existing file browser header already provides a device switcher; it must remain accessible from standalone windows.
- Promote the transfer task indicator/dock to an app-wide lightweight **传输任务** panel opened by a persistent compact activity control in the main header or navigation footer (with running count). Never discard or cancel transfers when the user navigates away.
- For discoverability, provide a secondary **浏览其他设备文件** action on the local share list rather than a new tab row. Preserve independent `filesWindow` deep links, transfer polling and ACL-verified remote inventory.
- Before implementing, validate cross-page mount/unmount lifetime for remote transfers, app-wide task polling and any OS Save As handles; those currently remain in `FileSharingPage` and cannot be dropped just by removing the tabs.

## Iteration 12: remove file-sharing tabs, preserve transfer owner, and prioritize lists elsewhere

**Implemented**
- Delete the **我的共享 / 其他设备 / 传输任务** tab strip from `FileSharingPage`. The main sidebar's **文件共享** route now lands directly in its existing, content-first local shared-folder list. The list header retains a compact **浏览其他设备文件** secondary action and **添加共享文件夹** primary action.
- Remote browsing remains device-first: **设备中心 → 共享文件** passes the exact authorized device ID into the persistent file workspace, while the remote explorer offers a small **我的共享** back action and the existing header device selector. No three-way navigation tabs are exposed.
- Replace the transfer tab with a global **传输任务** trigger in the Agent top bar, showing the number of running tasks. Opening it displays the existing per-task speed, progress, transport, pause/resume, save and clear actions in an accessible responsive drawer.
- **Critical lifetime constraint:** React mounts `FileSharingPage` exactly once under `App` and toggles only its visibility. Its `saveHandles`, `completedSaves`, `completedUploads`, upload staging, completion polling, and remote directory invalidation all remain live while switching to unrelated pages. The owner continuously polls transfers every 1.2 seconds (until Agent exit) to fulfill OS/browser Save As transfers without a special transfers route.
- A one-shot `navigationTarget` serial enables Device Center deep links without re-keying and thus remounting the transfer owner. The initial share selection still validates live ACLs; failure leaves the previous device ID unused and does not disclose another host's shares.
- CSS adapts the nested persistent remote explorer to fill the available Wails/standalone viewport. Focus mode retains its full-window intent. Preserved all existing file-browser V4 operations and upload/download RPCs.
- **Other pages:** change Messages' four segment buttons to a single compact message-type dropdown beside search. Move Monitor's four aggregate cards into a collapsible **流量统计与趋势** section below the connection table, opening directly on actual connection rows.
- Tests added: `file-sharing-workspace.test.js`, `secondary-controls.test.js`; updated `content-first-navigation.test.js`, `page-head-layout.test.js`.

**Acceptance / known scope:** Verify Windows/macOS file browser full-window and separate-window layout, Save As completion after navigating away from files, repeated reentry from Device Center, transfer drawer keyboard focus / Escape, and empty-share startup. The persistent transfer owner is an Agent React lifecycle implementation; it does not change Server/API semantics or attempt to persist browser file handles across full page reloads.

## Iteration 13: fixed target and progressive link checklist in device speed modal

- Device Center's **测速** shortcut always binds the speed dialog to the selected device ID; remove the redundant device/proxy selector and automatic/default option. Show device name and ID as **read-only** context and keep the test-duration input.
- Check **device authorization/online state**, then show the verified public-direct candidate count (an announced candidate does *not* prove the route works). The actual `goRunSpeedTest(exitId,duration)` invocation remains a single bidirectional RPC; this UI does **not** invent separate streaming probes.
- During that RPC, display a running check row for **双向链路测速** and leave Public Direct, P2P QUIC, Relay, upload and download evidence marked **待检测**. After the full response returns and the `exitId` matches, reveal each of these five check results one row at a time. Only a path the measured upload/download used gets a green check; a candidate is labelled **仅候选**, all other paths **未确认**, not failures.
- A failed test or wrong returned device ID displays the failure on the test step, leaving transport rows unverified. No fallback to another authorized or default proxy when the selected target disappears. Keep the existing five-round BBR calibration and the aggregate batch speed-test dialog.
- The local UPnP, Native UDP and control-session information stays in an explicit **本机网络环境** disclosure and is never passed off as a particular device's measured path.
- See `deviceSpeedChecks.js`, `deviceSpeedChecks.test.js` and updated `overview-scope.test.js`; ensure narrow Wails dialogs maintain visible device context, keyboard focus, and reduced-motion support.

## Iteration 14: zero-extra-row device selection for remote file browsing

- **Remote browser:** Replace the former separate **设备下拉框 + 当前目录标题 + 窗口操作** row. The *existing* 44px breadcrumb navigation now begins with a clickable current device chip (`设备名称 ▾`), followed by the verified share root and nested folder segments. Keep Back and Refresh on the same row; file search/sort remains in the content toolbar.
- Clicking the device chip opens a **floating, searchable chooser** rendered as a sibling of the breadcrumb strip, not inside its overflow-clipped `nav`. It filters the already-ACL-verified `peers` array by name or ID, shows authorized share count and shortened ID, highlights the active peer, and closes on selection, outside pointer or Escape. It does not modify proxy exit policy.
- Consolidate the former **我的共享** remote back-bar, native standalone Wails window, focus mode and same-tab standalone navigation into a compact **更多** menu at the right of that *same* breadcrumb row. Remove the obsolete remote file back-bar entirely. Preserve legacy `filesWindow/fileDevice/fileShare/filePath` deep links and Wails file-browser window API.
- At narrow widths, the device popover anchors at the bottom of the browser pane and overlays the file list instead of adding height. Left tree remains **only** the currently selected device's ACL-verified shares; right file list retains selection, upload/download, context menus and path navigation.
- **Local folder permissions:** Replace the long native candidate-device dropdown inside the existing shared-folder editor with a nested **添加授权设备** disclosure. Search only `goGetFileShareCandidates` (Server-approved, identity-filtered candidates), select multiple devices, and apply an explicitly selected **只读 (default) / 读写** permission. Existing grants can still be edited or removed individually; never expose unapproved devices. This picker stays inside the editor, not the list header.
- Tests: `compact-file-device-selection.test.js` and updated `content-first-navigation.test.js`, `file-sharing-workspace.test.js`. Native 800px, 125% scaling, browser focus-window and mobile popover positioning remain important visual acceptance cases.

## Iteration 15: single-line per-link speed checks

- Every row in the Device Center speed dialog now lays out **status icon | check name | concise evidence | result state** on **one nonwrapping horizontal line**. The previous two-line label/detail stack has been removed; full tooltips and accessible labels preserve untruncated diagnostic context.
- Check rows use a compact 38–40px grid with independently truncated label/evidence cells, a fixed status area and reduced 22–23px icons. A separate narrow-screen grid keeps all four pieces on one row instead of increasing dialog height.
- Only presentation changes: actual `goRunSpeedTest` execution, exact-exit evidence matching, check sequencing, failure/candidate semantics, per-direction throughput and BBR calibration remain unchanged.
- Regression test: `agent/gui/frontend/src/speed-check-density.test.js`.

## Iteration 16: collapsible speed-test inspection and richer shared-file glyphs

- **设备测速 → 链路逐项检查:** adds an accessible expand/collapse control; default expanded, and a persistent one-line live summary reports **测速中、结果核验中、检测失败** or the confirmed count even when the table is hidden. Collapsing hides the checklist and local network context, without canceling or changing the running test or BBR calibration. The arrow, status and focus styling are visible on compact windows.
- **文件共享图标:** expands deterministic extension/name classification from generic office/image/archive/code/text buckets to recognizable JavaScript, TypeScript, Python, Go, Rust, Markdown, JSON, web markup, CSS, notebooks, databases, structured data, vectors, design documents, 3D models, subtitles, disk images, electronic books and certificates. Both detailed and compact glyphs share the same scalable inline SVG base and semantic hues; default and generic folder rendering remain consistent.
- Extensions only, no remote content reads, thumbnails or additional file RPCs, ensuring large directory performance and preventing file preview security regressions. Existing file-selection, upload/download and share ACL flows are unchanged.
- Tests extended in `fileIconKind.test.js` and added in `checklist-collapse-and-icon-diversity.test.js`. Native small-Wails viewport and dark-mode visual QA remain recommended.

## Iteration 17: app-specific Office and archive illustrations

- **Word / Excel / PowerPoint** no longer share generic office glyphs. Legacy and current Microsoft extensions (DOC/DOCX/DOCM/DOT, XLS/XLSX/XLSM/XLSB/XLT, PPT/PPTX/PPTM/PPS/POT) resolve to independent, legible vector **W / X / P** symbols and distinct blue/green/orange palettes. LibreOffice/OpenDocument, Apple Pages/Numbers/Keynote, CSV/TSV continue with generic document/table/presentation marks.
- **ZIP / RAR / 7Z / TAR** render distinct colored archive-badge illustrations; TAR compound names (`.tar.gz`, `.tar.xz`, `.tar.zst`) and `.tgz` retain TAR classification, while a bare `.gz` or `.xz` is a compressed stream. Generic archives keep the original zipper symbol.
- **Windows installers, Android APK/AAB, Apple app bundles/IPA, Linux packages** receive distinct scalable SVG marks. Existing image, media, PDF, typography, design, developer and data icon families remain intact. Unknown types retain the document fallback; directory type always takes precedence over its name.
- The shared `FileGlyph` remains used throughout V4 file list, grid, details and directory tree. Changes are visual/extension-only, require no file content reads or additional RPCs, and preserve dark theme/focus mode and existing transfer/security behavior.
- Add classification coverage and `office-and-archive-icons.test.js` to guard iconic shape/color distinctions and suffix precedence.

## Iteration 18: single-row top actions on narrow windows

- The Agent's PageHead switches its action group from the topbar to inline display below 1100px; that inline variant previously used `flex-wrap:wrap` and created multiple action rows. Both modes now enforce `flex-wrap:nowrap`, zero minimum flex width, and horizontal **inner** scrolling when buttons exceed the available viewport. The page never grows wider, and the actions area is keyboard-focusable and labeled.
- Shared-folder list actions use the same no-wrap pattern, keeping the primary **添加共享文件夹** button first. Secondary browse/refresh and service state remain reachable by scrolling that small action region instead of pushing the folder list down.
- File Browser V4's search, sort, view, details, create-folder and upload controls remain on one line, with the search field retaining usable minimum width; this region scrolls independently of the directory tree and file list.
- The topbar's title and transfer-task control remain separate from page-action overflow. No control is removed and no file, proxy or transfer API behavior changes.
- `single-row-action-bars.test.js` protects the nowrap/overflow behavior; `page-head-layout.test.js` updated for the file-sharing header. Small Wails window and zoomed browser visual QA recommended.
