package gui

import "embed"

// assets holds the desktop UI. Everything is embedded so the shipped binary is
// fully standalone: Tailwind is inlined into the page at load time and the logo
// travels as a data URI, so the window needs no network and no sibling files.
//
//go:embed assets
var assets embed.FS

//go:embed assets/tailwind.js
var tailwindJS []byte
