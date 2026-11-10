package httpapp

import "embed"

//go:embed templates/*.html static/style.css
var assets embed.FS
