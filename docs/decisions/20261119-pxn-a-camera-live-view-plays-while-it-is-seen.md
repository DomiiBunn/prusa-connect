---
title: "ADR-20261119-pxn: A camera live view plays while it is seen"
description: "On the Cameras tab and the Now tab a camera's live view starts while its picture is on screen in a shown browser tab, and stops when it is not, unless the person stopped it."
date: 2026-11-19
status: proposed
supersedes: []
amends: [ADR-20261106-xm4]
tags:
  - adr
  - cameras
---
# ADR-20261119-pxn: A camera live view plays while it is seen

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md) | Amends [ADR-20261106-xm4](20261106-xm4-a-printer-page-has-a-cameras-tab-and-a-module-draws-under.md)

ADR-20261106-xm4 rule 3 starts a live view only when a person presses play.
A person who opens a camera's tab has come to watch it, so the press is one
step that everybody takes. The record rejected a view that starts when the tab
opens, because every camera would then stream to every open page for as long
as it stayed open.

That cost comes from a view nobody looks at. A view that runs only while
somebody can see it costs the camera nothing while nobody does.

## rules

1. On the Cameras tab and the Now tab, a camera's live view starts when its
   picture is on screen in a browser tab that is shown. It stops when the
   picture leaves the screen or the tab is hidden, and starts again when both
   are true again.
2. A person who presses stop holds the view stopped. It does not start again
   by itself until they press play.
3. A live view that does not start is held the same way. The picture says it
   did not start, and the person presses play to try again. A view is never
   retried by itself.
4. The dashboard's camera tile and the printer list's camera view still wait
   for play. They can draw many cameras at once, and a person looks at them
   for the still frames.
5. Rule 3 of ADR-20261106-xm4 otherwise holds. The still frame stays under
   the live view until the first frame covers it. The picture says it is live
   only while it plays.

## enforcement

The `whileSeen` tests hold rule 1: on screen and shown, each change reported
once, and nothing reported after the picture is gone. The `PrinterCamerasPanel`
tests hold rules 2 and 3.

## rejected

| option | why not |
|---|---|
| a live view that starts when the tab opens and runs until the page closes | the cost ADR-20261106-xm4 rejected: a stream for every page left open in a background tab |
| a setting each person turns on | everybody who opens the tab came to watch, so the setting would be one more step that everybody takes |
| a view that retries by itself after it did not start | a camera that is down would be asked again and again by every page that shows it |
| the same on the dashboard and the printer list | they draw many cameras at once, and each would stream while the page is open on a wall |
