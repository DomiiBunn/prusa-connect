---
title: "ADR-20261125-eeh: a browser with no credential gets a page at a module address"
description: "A browser that opens a module's address with no credential gets a page from the core with the module's title and summary, instead of the problem document."
date: 2026-11-25
status: proposed
supersedes: []
amends: []
tags:
  - adr
  - modules
  - auth
---
# ADR-20261125-eeh: a browser with no credential gets a page at a module address

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md)

An app that uses a module may open the module's address in a browser or a
web view with no credential. The JSON 401 then looks like a fault. A public
route in the module would answer it, but the operator would approve a route
anybody may call.

## rules

1. A browser that opens a module's address with no credential gets a page
   from the core instead of the 401 problem document. Every other client gets
   the problem document as before.
2. The page shows only the module's title and summary from its manifest. No
   module code runs, so the operator approves nothing.
3. The page links nowhere. An app may put its own credential into every page
   of its web view, and a link would pass it on.

## enforcement

| rule | gate |
|---|---|
| 1, 2, 3 | `TestABrowserWithNoCredentialGetsThePageOfTheModulesAddress` in `internal/transport/http/httpapi` |
| 1 | `TestOnlyABrowserWithNoCredentialGetsThePage` in `internal/transport/http/httpapi` |

## rejected

| option | why not |
|---|---|
| a public route in the module | the operator approves a route anybody may call |
| a link to the app on the page | it passes on the credential an app put into its web view |
