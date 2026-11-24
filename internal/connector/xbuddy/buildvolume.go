package xbuddy

import (
	"encoding/json"
	"strconv"

	"github.com/prusa3d/prusa-connect/sdk/connectorapi"
)

// buildVolume is the space a print may fill on one model, in millimetres.
type buildVolume struct {
	x, y, z float64
}

// buildVolumes is the printable volume of each model this driver reaches,
// keyed by the model it reports.
//
// The figures are the ones PrusaSlicer slices against: bed_shape and
// max_print_height of the printer profiles in its PrusaResearch.ini vendor
// bundle, which is public. A file sliced for the model fits inside them by
// construction. The iX and the INDX variants of the CORE One and CORE One L
// have no public slicer profile, so their figures are X_BED_SIZE, Y_BED_SIZE
// and Z_SIZE of the public firmware: include/marlin/Configuration_iX.h, and
// the HAS_INDX() branch of Configuration_COREONE.h and
// Configuration_COREONEL.h. An INDX bed differs from the plain model's, and
// the firmware keeps a strip of it for the nozzle cleaner and the tool dock,
// so those rows are an upper bound rather than the area a slicer would use.
// A model with no row reports no volume.
//
//nolint:gochecknoglobals,mnd // a fixed table, transcribed and read-only.
var buildVolumes = map[string]buildVolume{
	"MINI":     {x: 180, y: 180, z: 180},
	"MK3.5":    {x: 250, y: 210, z: 210},
	"MK3.5S":   {x: 250, y: 210, z: 210},
	"MK3.9":    {x: 250, y: 210, z: 220},
	"MK3.9S":   {x: 250, y: 210, z: 220},
	"MK4":      {x: 250, y: 210, z: 220},
	"MK4S":     {x: 250, y: 210, z: 220},
	"XL":       {x: 360, y: 360, z: 360},
	"CoreOne":  {x: 250, y: 220, z: 270},
	"CoreOneL": {x: 300, y: 300, z: 330},
	"IX":       {x: 260, y: 260, z: 175},

	"CoreOne_INDX":  {x: 250, y: 205.5, z: 275},
	"CoreOneL_INDX": {x: 301, y: 270, z: 330},
}

// byModel is the entry of a table keyed by model whose key names the same
// model, matched as the host matches one (connectorapi.FoldModel). A model that
// folds to nothing matches no entry.
func byModel[T any](table map[string]T, model string) (T, bool) {
	if folded := connectorapi.FoldModel(model); folded != "" {
		for name, value := range table {
			if connectorapi.FoldModel(name) == folded {
				return value, true
			}
		}
	}

	var none T

	return none, false
}

// buildVolumeOf is the volume of a model, and false for a model this driver
// has no figures for.
func buildVolumeOf(model string) (buildVolume, bool) {
	return byModel(buildVolumes, model)
}

// buildVolumePatches is what one model reports about its volume.
func buildVolumePatches(model string) []connectorapi.StatePatch {
	volume, known := buildVolumeOf(model)
	if !known {
		return nil
	}

	number := func(value float64) json.RawMessage {
		return json.RawMessage(strconv.FormatFloat(value, 'f', -1, 64))
	}

	return []connectorapi.StatePatch{
		{Path: connectorapi.BuildVolumeXPath, Value: number(volume.x)},
		{Path: connectorapi.BuildVolumeYPath, Value: number(volume.y)},
		{Path: connectorapi.BuildVolumeZPath, Value: number(volume.z)},
	}
}
