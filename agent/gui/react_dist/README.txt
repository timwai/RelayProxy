This directory is populated by agent/gui/frontend (Vite) before Windows release builds.
It intentionally contains no generated bundle in git. When index.html is absent, the Go
desktop shell falls back to the legacy embedded page so ordinary go test/go build remains usable.
