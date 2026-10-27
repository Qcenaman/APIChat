package ui

import "embed"

//go:embed index.html app.js app.css marked.min.js purify.min.js
var Files embed.FS
