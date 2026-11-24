package xbuddy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildVolumePatchesNameTheModelsVolume(t *testing.T) {
	t.Parallel()

	got := written(buildVolumePatches("coreone"))
	require.Equal(t, map[string]string{
		"/build_volume/x": "250", "/build_volume/y": "220", "/build_volume/z": "270",
	}, got, "the model is matched without case")

	require.Equal(t, map[string]string{
		"/build_volume/x": "250", "/build_volume/y": "205.5", "/build_volume/z": "275",
	}, written(buildVolumePatches("CoreOne_INDX")), "an INDX variant has a bed of its own")

	indx := map[string]string{"/build_volume/x": "250", "/build_volume/y": "205.5", "/build_volume/z": "275"}
	indxL := map[string]string{"/build_volume/x": "301", "/build_volume/y": "270", "/build_volume/z": "330"}

	require.Equal(
		t,
		indx,
		written(buildVolumePatches("COREONEINDX")),
		"the firmware spells an INDX without its underscore",
	)
	require.Equal(t, indxL, written(buildVolumePatches("COREONEL-INDX")), "the firmware spells an INDX L with a hyphen")
	require.Equal(t, indxL, written(buildVolumePatches("CoreOneL_INDX")))
	require.Equal(t, got, written(buildVolumePatches("COREONE")), "a plain CORE One is not an INDX")

	require.Empty(t, buildVolumePatches("MK2.5"), "a model with no figures reports nothing")
	require.Empty(t, buildVolumePatches(""), "no model matches nothing")
	require.Empty(t, buildVolumePatches("-_ "), "a model that folds to nothing matches nothing")
}
