package httpapp

import "embed"

//go:embed templates/*.html static/style.css static/copy.js static/display.woff2 static/body.woff2 static/mono.woff2
var assets embed.FS
