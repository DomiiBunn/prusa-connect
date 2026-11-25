---
title: "ADR-20261112-2eb: PrusaSlicer 3 reaches this server through a compatibility module"
description: "A first-party module answers PrusaSlicer 3 with the requests and replies of Prusa Account and Prusa Connect, and the page the slicer shows signs in without the slicer's credential."
date: 2026-11-12
status: proposed
supersedes: []
amends: []
tags:
  - adr
  - auth
  - extensions
---
# ADR-20261112-2eb: PrusaSlicer 3 reaches this server through a compatibility module

<!-- nav:related -->
> [!NOTE]
> Index: [decisions](README.md) | Builds on [ADR-20261025-tpp](20261025-tpp-a-credential-carries-scopes-and-an-app-asks-a-person-for-one.md), [ADR-20260827-wmv](20260827-wmv-a-module-is-mounted-and-an-adapter-is-constructed.md), [ADR-20261120-xys](20261120-xys-a-module-route-may-take-no-caller-or-a-large-body-written.md), [ADR-20261123-vvb](20261123-vvb-the-core-deletes-a-file-a-module-hands-it.md)

PrusaSlicer 3 signs in to Prusa Account and sends prints to Prusa Connect. A
person who runs this server and no cloud account should get the same features
from the same slicer. PrusaSlicer gets an offline mode, where the person types
the address of this server. In that mode the slicer sends the same requests to
other paths. Everything else stays as it is with the cloud. That is the
request and reply bodies, the error bodies and the claims in the token. It is
also the messages between the slicer and the page it shows.

The module `extensions/prusaslicer3-connector` gives those answers. An operator switches
it on from the marketplace. The slicer signs a person in through the app
pairing of this server
([ADR-20261112-nay](20261112-nay-an-app-pairing-credential-also-travels-in-a-jwt-shape.md)),
not through the module. The module never talks to Prusa Account.

The module uses two things the host gives every module
([ADR-20261120-xys](20261120-xys-a-module-route-may-take-no-caller-or-a-large-body-written.md)).
The G-code upload is a staged route, and the avatar the slicer downloads
without a credential is a public route. The module hands the uploaded file
to the core to print, and the core deletes it once the print has settled
([ADR-20261123-vvb](20261123-vvb-the-core-deletes-a-file-a-module-hands-it.md)).

## rules

### what the module answers

1. The module answers the requests PrusaSlicer 3 makes to Prusa Account and to
   Prusa Connect, under its own mount. The paths differ from the cloud. The
   bodies, the status codes and the error bodies match the cloud. A request
   the server refuses before the module runs gets the server's problem
   document and not the cloud's error body. That is a missing or ended
   credential, the extension's access check, and the refusals of a staged
   route (ADR-20261120-xys).
2. The module's README lists the requests in the order they run, with each
   request and its reply.
3. The module never calls Prusa Account or Prusa Connect.

### the page inside the slicer

4. The frontend checks for `window._prusaSlicer` when it starts. When it is
   there, it installs `window._prusaConnect_v2` with `init` and `logout`, and
   asks the slicer for its configuration. The slicer opens a page session
   and sets its HttpOnly cookie in the web view itself. The page never
   receives the slicer's credential (ADR-20261112-nay rules 6 to 16). A
   refusal with status 401 asks the slicer again.
5. `requestCompatiblePrinter` becomes a browser event, and a module page
   listens for it.
6. A module page or the app pairing page (`/pair`) shown with `?embed=1`
   renders without the sidebar and the header. The slicer shows the app
   pairing page in its own window, and the person must not leave the
   approval through the navigation. Every other core screen keeps them.

## enforcement

| rule | gate |
|---|---|
| 1, 2 | the contract tests in `extensions/prusaslicer3-connector`, against recorded cloud replies |
| 1 | `TestAViewersPrintIsRefusedAsTheCloudRefusesIt` in the module's `connect` package |
| 4, 5 | `frontend/src/lib/slicer/bridge.svelte.test.ts` and `packages/svelte/src/slicer/slicer.test.ts` |
| 6 | `frontend/src/lib/embed.test.ts` and `frontend/src/app-slicer.svelte.test.ts` |

## rejected

| option | why not |
|---|---|
| the module signs its own tokens and the host verifies them | the host would trust a key a module holds |
| an OAuth authorization server in the core | ADR-20261025-tpp rejects it. The app pairing already gives the slicer a credential a person approved |
| the module emulates the Prusa Account OAuth flow | it repeats the app pairing with two more host methods, and the module would issue and renew a credential for a person |
| the cloud paths at the origin root | an operator mount is one prefix, and the slicer's offline mode takes any path |
| a module listener on its own port for the HTTP routes | ADR-20260910-cqq rule 5 forbids serving this server's HTTP there |
| the module checks the token itself | a module never holds a secret it could check a token with, so the host checks it |
| an MQTT broker in the core | the core has no broker, and only this module's client needs one |
| an MQTT broker in the module, on a port the host serves over TLS | the slicer reads each printer's state from the printer list every 30 seconds, and a broker is one more open port |
| the upload through the core storage API | the slicer reads the cloud's upload reply and error body. The module has to answer them |

## open items

- The core has no print queue. The module prints at once or keeps the file.
  A queue position is refused with the cloud's error until a queue exists.

