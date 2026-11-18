//go:build ui

package www

import "embed"

//go:embed all:dist
var distFS embed.FS
