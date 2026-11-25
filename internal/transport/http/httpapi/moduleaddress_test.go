package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/authz"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
)

// addressServer is one module titled the way a manifest may title it, behind
// a graph that permits only the operator, called as who.
func addressServer(t *testing.T, who string, reached *bool) *Server {
	t.Helper()

	instance, err := ref.Parse(moduleInstance)
	require.NoError(t, err)

	plane := fakeModulePlane{
		namespace: moduleNamespace,
		instance:  instance,
		title:     `Planner <script>alert(1)</script>`,
		summary:   "Plans prints for every printer.",
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			*reached = true
			w.WriteHeader(http.StatusOK)
		}),
	}

	access := &fakeModuleAccess{
		who:      "user:operator",
		instance: instance,
		actions:  []authz.Action{authz.ActionViewResource},
	}

	return moduleServer(t, plane, access, who)
}

func openAddress(server *Server, accept, language string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/modules/"+moduleNamespace+"/printers/p1/", nil)
	request.Header.Set("Accept", accept)
	request.Header.Set("Accept-Language", language)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	return recorder
}

// A browser that opens a module's address with no credential reads whose
// address it is, from the core's own page, and the module never runs.
func TestABrowserWithNoCredentialGetsThePageOfTheModulesAddress(t *testing.T) {
	t.Parallel()

	const browser = "text/html,application/xhtml+xml,*/*;q=0.8"

	for _, tc := range []struct{ language, says string }{
		{"", "An extension of Prusa Connect (local)."},
		{"cs-CZ,cs;q=0.9", "Rozšíření Prusa Connect (local)."},
	} {
		var reached bool

		answer := openAddress(addressServer(t, "", &reached), browser, tc.language)

		require.Equal(t, http.StatusUnauthorized, answer.Code)
		require.Equal(t, "text/html; charset=utf-8", answer.Header().Get("Content-Type"))
		require.Equal(t, moduleAddressCSP, answer.Header().Get("Content-Security-Policy"))
		require.Equal(t, "nosniff", answer.Header().Get("X-Content-Type-Options"))

		body := answer.Body.String()
		require.Contains(t, body, tc.says)
		require.NotContains(t, body, "<script>", "the title went into the page unescaped")
		require.Contains(t, body, "&lt;script&gt;")
		require.NotContains(t, body, "<a", "the page links somewhere")
		require.NotContains(t, body, "href", "the page links somewhere")

		require.Contains(t, body, "Plans prints for every printer.")

		require.False(t, reached, "the module ran for a caller with no credential")
	}
}

// An app that asks for no HTML, and a signed-in person the graph refuses,
// still get the problem document.
func TestOnlyABrowserWithNoCredentialGetsThePage(t *testing.T) {
	t.Parallel()

	var reached bool

	app := openAddress(addressServer(t, "", &reached), "*/*", "")
	require.Equal(t, http.StatusUnauthorized, app.Code)
	require.Equal(t, problemMediaType, app.Header().Get("Content-Type"))

	stranger := openAddress(addressServer(t, "user:stranger", &reached), "text/html", "")
	require.Equal(t, http.StatusForbidden, stranger.Code)
	require.Equal(t, problemMediaType, stranger.Header().Get("Content-Type"))

	require.False(t, reached)
}
