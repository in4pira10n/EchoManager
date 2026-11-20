package web

import (
	"embed"
	"io/fs"
)

//go:embed *.html *.webmanifest *.js *.css static/assets static/i18n.js static/strings
var assets embed.FS

func Files() fs.FS {
	return assets
}
