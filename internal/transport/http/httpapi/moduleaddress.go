package httpapi

import (
	"html/template"
	"net/http"
	"strings"
)

// A browser that opens a module's address with no credential gets a page
// instead of the problem document, so a person sees whose address it is.
// PrusaSlicer's Physical printer tab is one: it opens the address it sends
// prints to, with no API key. The core writes the page from its own
// template and the module's title and summary. No module code runs, and the
// page reads nothing behind the address, so a module needs no public route
// for it. An app gets the problem document as before, because it does not
// ask for HTML.

// moduleAddressPage is the page, in English or Czech.
//
//nolint:gochecknoglobals // a parsed template, read and never written.
var moduleAddressPage = template.Must(template.New("address").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif}
main{max-width:36rem;padding:1.5rem;line-height:1.5}
h1{font-size:1.25rem}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{if .Summary}}<p>{{.Summary}}</p>
{{end}}<p>{{.Note}}</p>
</main></body></html>
`))

// moduleAddressCSP lets the page use its own inline style and nothing else.
const moduleAddressCSP = "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'"

// wantsAddressPage reports whether r is a browser opening the address: a GET
// or HEAD that asks for HTML.
func wantsAddressPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// writeModuleAddressPage answers status with the page for the module of
// surface: its title, its summary and a line that it is an extension. The
// summary is the manifest's, in English only, so only that line is in Czech.
func writeModuleAddressPage(w http.ResponseWriter, r *http.Request, surface ModuleSurface, status int) {
	title := surface.Title
	if title == "" {
		title = surface.Instance.ID()
	}

	page := struct{ Lang, Title, Summary, Note string }{
		Lang:    "en",
		Title:   title,
		Summary: surface.Summary,
		Note:    "An extension of Prusa Connect (local).",
	}

	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "cs") {
		page.Lang = "cs"
		page.Note = "Rozšíření Prusa Connect (local)."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", moduleAddressCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if headRequest(r) {
		return
	}

	_ = moduleAddressPage.Execute(w, page)
}
