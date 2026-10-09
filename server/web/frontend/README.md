# Server Web · React Console

This React/Vite frontend implements the Server console prototype in
`docs/prototypes/relayproxy-server-web-prototype.html`, backed by existing `/api/v1` endpoints.

```
cd server/web/frontend
npm install
npm test
npm run build
```

The Vite build writes to `server/web/react_dist/`, which the Go HTTP handler embeds.
While the directory has no `index.html`, the legacy administrative UI remains the
root console. Once built, React is served at `/` and the untouched legacy console
is still available at `/classic` for backwards compatibility or recovery.

For local development use `npm run dev` (proxy default `127.0.0.1:21080`).
Auth uses same-origin cookies and API permissions; admin-only pages are not
shown to identity-scoped users, and the server remains the final authority.

The React console covers overview, enrollment/device authorization, identity
administration, exit status, sessions/P2P, message history, channel rules,
RDP ingress/audit/security, and all 41 server configuration fields. Legacy
RDP records and deep diagnostics can also be managed in `/classic` during
migration. The `main` bundle must be built after modifying any React source.