---
title: "ADR-20261125-kyn: an operator hides no settings tab and no page kept out of the sidebar"
description: "A module's settings tab and its card on a printer's Printer tab are where the module is set up, so they cannot be hidden. A page kept out of the sidebar has no entry to hide, so the Where it shows tab does not list it."
date: 2026-11-25
status: proposed
supersedes: []
amends: [ADR-20261021-3dp, ADR-20261009-wax]
tags:
  - adr
  - modules
  - ui
---
# ADR-20261125-kyn: an operator hides no settings tab and no page kept out of the sidebar

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md) | Builds on [ADR-20261021-3dp](20261021-3dp-an-operator-hides-a-file-action-or-a-print-section.md), [ADR-20261009-wax](20261009-wax-an-operator-hides-a-module-contribution.md)

A module's settings tab sits on the module's own page under Settings,
Extensions. The PrusaSlicer connectors draw a How to tab there, which tells
the operator where the connector is set up. The Where it shows tab offered a
switch that hid it, so the operator could hide the one place that explains
the module, from themselves.

The PrusaSlicer 2 Connector's card on a printer's Printer tab is where a
person sets the connector up for that printer. Hiding it leaves a connector
nobody can set up. An operator who does not want it removes the module.

The same tab listed a page the manifest keeps out of the sidebar, such as the
print dialog PrusaSlicer 3 opens by its address. Hiding a page removes its
sidebar entry, and such a page has none, so the switch did nothing.

## rules

1. An operator may not hide an entry of the `settings` slot. This is the
   exception ADR-20261021-3dp rule 4 asks to be named.
2. An operator may not hide an entry of the `device-info-section` slot either.
   An operator who does not want it removes the module.
3. The core drops a `settings/<id>` or `device_info_section/<id>` entry of
   `hidden_contributions`, on a write and on a read, rather than refusing it.
   A declaration written before this record, through the API or typed into
   `config.yaml`, still saves, and the entry hides nothing.
4. The Where it shows tab lists no settings tab, no Printer tab card and no
   page whose manifest entry is `hidden`. A module with nothing left to list
   has no Where it shows tab.
5. A running module may still state its own settings tab or card as hidden. That is
   the module's choice and not the operator's.

These rules amend ADR-20261021-3dp rules 1 and 4: `settings` and
`device_info_section` join `cards` as slot keys that are not hideable. Rule 4
amends ADR-20261009-wax rule 5: the extension page lists every entry an
operator can hide, and no entry it cannot.

## enforcement

| rule | gate |
|---|---|
| 1, 2, 3 | `TestAnEntryThatCanNoLongerBeHiddenIsDropped` in `internal/app/resource` |
| 3 | `TestAHandWrittenEntryThatCanNoLongerBeHiddenIsDroppedOnRead` in `internal/app/resource` |
| 4 | `contributions.test.ts` in `frontend/src/lib/extensions/extension-visibility` |
| 5 | none. `hidden` on a module's UI status still takes both slot keys |

## rejected

| option | why not |
|---|---|
| hide only the How to tab from the list | a rule for one tab id is a rule for one module; every settings tab is where the operator sets the module up |
| keep the switch for a hidden page | it changes nothing a person can see |
| refuse an entry of the two slots | a declaration that already hid one would refuse every later write, and its module's page could not be saved |
