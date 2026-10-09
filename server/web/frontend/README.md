# RelayProxy Server Web · React

React/Vite is now the only Server management console.

Build the frontend before packaging or launching a Server that serves the UI:

```sh
cd server/web/frontend
npm install
npm test
npm run build
```

The build writes to `server/web/react_dist/`, which is embedded into the Go binary.
If no bundle is present, `/` returns HTTP 503 with an actionable build instruction;
it does not silently serve an obsolete console. The `/classic` route and old HTML,
CSS and JavaScript are removed.

During development, use `npm run dev` with the API proxy to `127.0.0.1:21080`.
This frontend uses the existing `/api/v1` endpoints, session cookies and backend
authorization for devices, identity, exits, messages, RDP and Server settings.
