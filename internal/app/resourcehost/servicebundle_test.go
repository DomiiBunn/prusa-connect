package resourcehost_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/kernel/condition"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
)

// A module that ships both halves is one declaration, one instance and one
// namespace (ADR-20260911-4p6 rule 30). The process is launched and supervised as
// before, and the directory beside it is verified and served under the same
// mount, at /ui/.
//
// The child is the example service module these tests already build, and the
// bundle is a directory this file writes, so the case drives the real store,
// the real supervision plane and a real second process.

// declareBothHalves writes the one block `module add` writes for a module with
// a process half and a browser half.
func (w *wired) declareBothHalves(module *installed, bundleDir, bundleChecksum string, enabled bool) {
	w.t.Helper()

	w.writeFile(fmt.Sprintf(`
resources:
  sink:
    example:
      type: %s
      runtime: service
      enabled: %t
      service:
        path: %s
        checksum: %s
        contract_major: 1
        assets:
          path: %s
          checksum: %s
`, mountedModuleType, enabled, filepath.ToSlash(module.dir), module.checksum,
		filepath.ToSlash(bundleDir), bundleChecksum))

	require.NoError(w.t, w.store.Reload(w.t.Context()))
}

// fetchThrough drives one request at a module's own surface, the way the mount
// does: the group prefix is already off, so the path is the module's own.
func fetchThrough(t *testing.T, handler http.Handler, path string) *http.Response {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

	return recorder.Result()
}

// TestAModuleWithBothHalvesServesRoutesAndFilesUnderOneNamespace is the whole
// of rule 30 at the plane: one declaration, one supervised instance, the
// routes and the files under one namespace, and one entry in the listing that
// carries the ui block the bundle's manifest declared.
func TestAModuleWithBothHalvesServesRoutesAndFilesUnderOneNamespace(t *testing.T) {
	t.Parallel()

	w := newWired(t)
	module := install(t)
	instance := theServiceSink(t)
	bundleDir, bundleChecksum := aBundleWith(t, theUIBlock)

	w.declareBothHalves(module, bundleDir, bundleChecksum, true)
	w.waitReady(instance)

	// One instance, not two. The browser half is not a second declaration, so
	// nothing of this module is supervised under the asset kind.
	require.Len(t, w.host.Supervising(ref.KindSink), 1)
	require.Empty(t, w.host.Supervising(ref.KindAsset))

	surface, claimed := w.host.Module(mountedNamespace)
	require.True(t, claimed, "the namespace the type produced is not mounted")
	require.NotEmpty(t, surface.OpenAPI, "the mount carries no document for a module that shipped one")

	// The files, under the module's own mount, with the bundle's digest as the
	// entity tag. A page that imports the entry reads exactly this.
	answered := fetchThrough(t, surface.Handler, "/ui/index.js")
	require.NoError(t, answered.Body.Close())
	require.Equal(t, http.StatusOK, answered.StatusCode)
	require.Equal(t, "text/javascript; charset=utf-8", answered.Header.Get("Content-Type"))
	require.Contains(t, answered.Header.Get("ETag"), bundleChecksum[len("sha256:"):])

	// A name that is not in the bundle is not passed to the child: the subtree
	// is the host's, and the module has no route there.
	missing := fetchThrough(t, surface.Handler, "/ui/nothing.js")
	require.NoError(t, missing.Body.Close())
	require.Equal(t, http.StatusNotFound, missing.StatusCode)

	// And the listing's half: one line, with the ui block of the bundle's own
	// manifest and an entry address built from the same namespace.
	drawn, ok := w.host.UI(instance)
	require.True(t, ok, "a ready module with a bundle draws nothing")
	require.Equal(t, "/api/v1/modules/"+mountedNamespace+"/ui/index.js", drawn.EntryURL)
	require.Equal(t, ">=0.3.0 <1.0.0", drawn.Library)
	require.Len(t, drawn.Contributes.Pages, 1)
}

// TestDisablingAModuleWithBothHalvesStopsBoth is the one enabled flag of rule
// 30. The operator switches one instance off and the routes and the files go
// together, because there is only ever one instance to switch.
func TestDisablingAModuleWithBothHalvesStopsBoth(t *testing.T) {
	t.Parallel()

	w := newWired(t)
	module := install(t)
	instance := theServiceSink(t)
	bundleDir, bundleChecksum := aBundleWith(t, theUIBlock)

	w.declareBothHalves(module, bundleDir, bundleChecksum, true)
	w.waitReady(instance)

	_, claimed := w.host.Module(mountedNamespace)
	require.True(t, claimed)

	w.declareBothHalves(module, bundleDir, bundleChecksum, false)

	w.await(func() bool {
		_, held := w.host.Module(mountedNamespace)

		return !held
	}, "the module's routes are still mounted after it was disabled",
		func() string { return w.describe(instance) })

	_, drawn := w.host.UI(instance)
	require.False(t, drawn, "a disabled module still publishes a bundle to import")
}

// TestAModuleWhoseBundleChangedNeverServes holds the bundle the way the
// artefact is held. The digest covers the directory, so a file edited after
// the install leaves the whole module refused: the instance latches failed
// with the code a swapped artefact carries, the listing publishes no entry to
// import, and no file is served.
func TestAModuleWhoseBundleChangedNeverServes(t *testing.T) {
	t.Parallel()

	w := newWired(t)
	module := install(t)
	instance := theServiceSink(t)
	bundleDir, _ := aBundleWith(t, theUIBlock)
	_, otherChecksum := aBundleWith(t, "name: other\nkind: asset\nlicense: MIT\n")

	w.declareBothHalves(module, bundleDir, otherChecksum, true)

	status := w.waitState(instance, condition.StateFailed())
	require.True(t, says(status, "RESOURCE_PLUGIN_CHECKSUM"), conditionsOf(&status))
	require.False(t, status.Serving)

	_, drawn := w.host.UI(instance)
	require.False(t, drawn, "a module whose bundle failed its digest published an entry to import")

	surface, claimed := w.host.Module(mountedNamespace)
	require.True(t, claimed, "the namespace stays claimed while the instance is declared")

	answered := fetchThrough(t, surface.Handler, "/ui/index.js")
	require.NoError(t, answered.Body.Close())
	require.NotEqual(t, http.StatusOK, answered.StatusCode, "a file of an unverified bundle was served")
}

// The surface carries the module's title and summary from its declaration,
// for the page a browser with no credential reads at the module's address.
func TestTheSurfaceCarriesTheModulesTitleAndSummary(t *testing.T) {
	t.Parallel()

	w := newWired(t)
	module := installAs(t, pluginName)
	instance := theServiceSink(t)

	w.writeFile(fmt.Sprintf(`
resources:
  sink:
    example:
      type: %s
      runtime: service
      enabled: true
      module:
        version: 1.0.0
        title: Planner
        summary: Plans prints for every printer.
      service:
        path: %s
        checksum: %s
        contract_major: 1
`, mountedNamespace+"/"+pluginName, filepath.ToSlash(module.dir), module.checksum))
	require.NoError(t, w.store.Reload(t.Context()))
	w.waitReady(instance)

	surface, claimed := w.host.Module(mountedNamespace)
	require.True(t, claimed)
	require.Equal(t, "Planner", surface.Title)
	require.Equal(t, "Plans prints for every printer.", surface.Summary)
}
