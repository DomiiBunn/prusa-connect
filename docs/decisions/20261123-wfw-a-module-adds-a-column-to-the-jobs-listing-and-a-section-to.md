---
title: "ADR-20261123-wfw: a module adds a column to the Jobs listing and a section to a print's page"
description: "Two slots, job-column and job-section, hand a module the id of a print, so a module can show what it knows about each print in the Jobs listing and on the print's page."
date: 2026-11-23
status: proposed
supersedes: []
amends: [ADR-20260911-4p6]
tags:
  - adr
  - frontend
  - modules
---
# ADR-20261123-wfw: a module adds a column to the Jobs listing and a section to a print's page

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md) | Builds on [ADR-20260911-4p6](20260911-4p6-a-module-extends-the-ui-through-one-library-and-four-slots.md), [ADR-20261112-62y](20261112-62y-a-module-draws-a-printer-control-under-the-header-and-on.md), [ADR-20261124-e2e](20261124-e2e-a-module-draws-a-section-on-a-printer-s-printer-tab.md)

A module often knows something about each print that the core does not. A
filament module counts the grams each print took off each spool. A cost
module prices a print. Neither has a place to show it: no slot is on the Jobs
listing or on a print's page.

The core cannot show these values itself. It does not know what a module
keeps, and it must not learn: each module keeps its own data. So the core
offers two places and hands each the print's id, and the module draws its
value there.

A component draws nothing for a print it knows nothing about. Each cell is
handed one print. A column component that reads its values from a server can
gather the prints its cells ask for, and read them in one request.

## rules

1. The subject `job` hands the id of one print. It is the id the core's
   `/api/v1/jobs` routes and the `device.job.started` and `device.job.settled`
   events carry, so a module can file its own data under it.
2. The slot `job-column` is a column of the module's own in the Jobs listing,
   after the core's columns. It is a section with a job subject. The entry's
   title is the column's heading.
3. The host reads the headings from the manifests, not from what a bundle
   registers. The table's columns are then known before any bundle loads, and
   the table does not move when one loads.
4. Each cell is the `job-column` outlet for that entry and that print. The
   host draws no frame or heading around the value: the table cell is the
   frame. The cell stays under the row's link, as the core's cells do, so a
   press on the value opens the print.
5. A module's column folds away in a narrow table, as Started, Duration and
   Progress do. The narrow rows view shows the module's values after the
   core's.
6. The slot `job-section` is a section on a print's page, under the core's
   details. It is a section with a job subject, drawn in the `card` layout of
   ADR-20261124-e2e: a card with the entry's title as its heading. A card whose
   component draws nothing is hidden with its title, so a print the module
   knows nothing of shows no empty card.
7. These two slots join the set of ADR-20260911-4p6 rule 48 and the slots later
   records added. `sdk/manifest/slots.go` lists the whole set.

## enforcement

| rule | gate |
|---|---|
| 1, 2, 6, 7 | `make slots-check`, and `slot-outlets.svelte.test.ts` under `make frontend-check` |
| 2, 3, 4, 5 | `draws a module column with its heading and one cell a print` in `jobs-screen.svelte.test.ts` |
| 3 | `the Jobs listing opened first` in `slot-outlets.svelte.test.ts` |
| 6 | `hides the card of a job section that draws nothing` in `slot-outlet.svelte.test.ts` |

## rejected

| option | why not |
|---|---|
| the core shows the filament a print used | the core knows only the machine's own total. The grams off each spool, and what they cost, are a module's data |
| a module column in the listing's column chooser | the listing reads its fields once, when the screen is built. [#107](https://github.com/prusa3d/prusa-connect/issues/107) tracks it |
| headings from what the bundle registers | the table would gain a column after it is drawn, and every row would move |
| one slot for the listing and the page | a cell holds one short value, and a section holds a breakdown. A module may want one and not the other |
