---
title: "ADR-20261124-kty: an INDX is built with four heads or eight"
description: "The Prusa driver counts an INDX as four heads unless the firmware lists a fifth, and finds a model by the host's model fold, so the firmware's COREONEINDX is an INDX."
date: 2026-11-24
status: proposed
supersedes: []
amends: [ADR-20261023-gna]
tags:
  - adr
  - connectors
---
# ADR-20261124-kty: an INDX is built with four heads or eight

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md) | Builds on [ADR-20261023-gna](20261023-gna-a-printers-tools-are-a-tree-of-heads-and-feeder-slots.md)

An INDX ships with four heads or with eight, and its firmware counts eight for
both. The firmware names the model `COREONEINDX` or `COREONEL-INDX`, which the
driver's tables did not match, so a real INDX was an unknown model.

## rules

1. The Prusa driver finds a model in its own tables by `connectorapi.FoldModel`,
   the fold the host matches models by. `COREONEINDX`, `CoreOne_INDX` and
   `COREONE_INDX` name one model.
2. An INDX is built with four heads or with eight. When the firmware lists its
   tools by number, `/tool_count` is four unless it lists an entry above four,
   and then eight. This is the rule Prusa Connect applies when it picks print
   files for an INDX.
3. An XL stays five heads. A model the driver has no head count for takes
   `/tool_count` from the highest entry the firmware lists.

These rules amend the section "How the Prusa driver reads two shapes" of
ADR-20261023-gna, which made an INDX eight heads. A firmware that sends the
tree keeps sending its own `tool_count`, which the driver copies unchanged.

## enforcement

| rule | gate |
|---|---|
| 1 | `TestBuildVolumePatchesNameTheModelsVolume` in `internal/connector/xbuddy` |
| 1, 2 | the INDX cases of `TestTheNumberedInfoBecomesTheTree` in `internal/connector/xbuddy` |
| 3 | the XL and unknown-model cases of `TestTheNumberedInfoBecomesTheTree` |

## rejected

| option | why not |
|---|---|
| fold the model name and keep eight heads | a four-head INDX shows eight heads, four of them switched off, and a slicer is offered its eight-head profile |
| add the firmware spellings as rows of their own | the host already treats every spelling with one fold as one model; a second rule drifts from it |
