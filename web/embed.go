// Package web embeds the browser UI. Vendored preview libraries land in
// vendor/ at build time (tools/vendor.py); the server tolerates their absence.
package web

import "embed"

//go:embed *
var Files embed.FS
