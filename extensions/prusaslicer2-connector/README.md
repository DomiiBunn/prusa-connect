# PrusaSlicer 2 Connector

This module lets PrusaSlicer 2.4 and later send a print to each printer of
this server, the way it sends one to a printer with PrusaLink. PrusaSlicer 2
does not know a Prusa Connect server, and the cloud mode of PrusaSlicer 3 is
the job of [prusaslicer3-connector](../prusaslicer3-connector/README.md). Here each
printer of this server gets an address of its own, and PrusaSlicer talks to
that address as if it were the printer.

It is set up for each printer, not for the server. Each printer has its own
address, and its own PrusaSlicer 2 Connector card with the steps, the API key
and Save to storage. The card is on the printer's page, on its Printer tab,
under the Connection card. The largest upload is set for the whole server, on
the extension's Uploads tab under Settings, Extensions.

## Setting it up in PrusaSlicer

1. Open the printer's page on this server. On its Printer tab, under the
   Connection card, the PrusaSlicer 2 Connector card shows the address and the button
   **Create an API key**.
2. In PrusaSlicer, click **Add physical printer** next to the printer profile.
3. Set **Host Type** to PrusaLink and paste the address into
   **Hostname, IP or URL**. It looks like
   `http://192.168.1.20:8080/api/v1/modules/<namespace>/printers/<id>/`.
4. Set **Authorization Type** to API key and paste the key.
5. Click **Test**. **Upload and Print** then sends the file to the printer
   and starts it, and **Upload** puts it on the printer without starting it.

One key works for every printer its person may operate, so a person with
several printers adds one physical printer for each and may paste the same
key into each. The key is an API credential of this server with the operate
scope. It is listed under **Settings**, **API credentials**, where the person
revokes it.

PrusaSlicer's **Browse** button looks only for OctoPrint on the network, so it
finds no printer of this server. The person pastes the address.

PrusaSlicer's **Physical printer** tab opens the address in a web view and
sends no API key with it. The core answers that tab with a short page that
names this extension, with its summary.

## The requests

Every path is under the module's mount,
`/api/v1/modules/<namespace>/printers/{printer}/`. PrusaSlicer adds the
PrusaLink path to the address the person pasted, and sends the key in
`X-Api-Key` on every request. The core resolves the person from the key before
the module runs, and refuses a request with no key or an unknown one with 401.

| request | PrusaSlicer sends it | the module answers |
|---|---|---|
| `GET api/version` | on **Test**, and before each upload | `api`, `text: PrusaLink`, the printer's name and `upload-by-put: true`, or 404 |
| `GET api/v1/storage` | before the upload dialog | one storage, `/local`, never read-only and never full |
| `PUT api/v1/files/local/{name}` | 2.6 and later, with the file as the body | 201. With `Print-After-Upload: ?1` the printer starts the file, and without it the file only lands |
| `POST api/files/local` | 2.5 and older, with the file in a form | the same, with `print=true` in place of the header |

The version answers 404 when the printer is not there or the person may not
see it, so **Test** fails on a wrong address.

The host writes the body of both upload routes to the module's data folder
before the module runs, so a file of hundreds of megabytes costs the module no
memory. The operator approves the limit at install, 1024 MiB as the module
asks, and may change it on the extension's Uploads tab.

### Upload and Print

The module checks the name, asks the core whether the person may operate the
printer, and checks that the printer is idle, ready, finished or stopped. It
then moves the file into its print folder and asks the core to send it to the
printer's USB drive and start it. PrusaSlicer always asks to overwrite, and
the printer keeps the file it has. So a printer that holds a file of that
name already gets the new one under a numbered name, such as
`benchy[1].gcode`. The core picks it, as for a copy from this server's pages.

A print the core refuses before the printer starts reading the file leaves
nothing behind. After any other failure, such as PrusaSlicer going away
mid-call, the printer may be reading the file, so it stays until the sweep.

A refusal is JSON with a `title` and a `message`, and PrusaSlicer shows both:

| status | when |
|---|---|
| 400 | the name holds a character a printer refuses, the form names a folder, or the file is empty |
| 403 | the key lacks the operate scope, or its person may not operate the printer or write into the Save to storage storage |
| 404 | no such printer or storage |
| 409 | the printer is not ready or refused the file, or the Save to storage folder holds every free name |
| 415 | the file is not `.gcode` or `.bgcode` |
| 503 | the module is starting, the Save to storage storage is gone, or the module failed and logged why |

The printer downloads the file from the module's print folder. The core
removes it from there once it landed, and a file the core did not remove is
swept after a day. The copy on the printer's USB drive is the printer's own
and stays.

### Upload

The file goes to the printer's USB drive the way Upload and Print sends it,
under a numbered name beside a file of that name, and nothing starts. The
printer may be busy, as with PrusaLink. The core removes the file from the
print folder once it landed.

### Last seen

The card says when PrusaSlicer last reached the printer here, to the minute,
or that it has not yet. PrusaSlicer holds no connection open. It asks on
**Test** and before each upload, so the card shows the last of those and not
whether the slicer is open now. The time survives a restart of the server.

### Save to storage

The printer's card on its Printer tab has the same Save to storage switch as
a file dropped on the printer's page. It is one setting for the printer, and
it applies to every upload from PrusaSlicer to it, whoever sends it. A person
who may operate the printer turns it on and picks the storage and folder.
Without the `operate` check the module cannot tell who that is, so nobody may
change the setting.

With it on, each upload to that printer, with or without a print, is written
there as the person whose API key sent it. It goes to the printer from there,
and the copy stays on this server. A file of that name already in the folder
stays, and the upload is saved beside it as `name[1].bgcode`. Two uploads
of one name in the same moment may still find the same free name, and then
one replaces the other on this server. On the printer both stay. A file
saved this way stays in the storage even when the printer then refuses it,
so sending it again saves it beside the first as `name[1]`. A person who may
not write into that storage gets 403, and a storage that is gone gets 503. So
a storage only some people may write to blocks the uploads of the others.
Off, the file is deleted here once it landed.

## What the operator approves at install

- the data folder, for the staged bodies and the print folder
- the `operate` check, so a person who may only view a printer is refused
  before the printer is asked
- the two staged upload routes, with their limit
- the `Print-After-Upload` header, which the module requires. Without it,
  Upload and Print would put the file on the printer and still report a
  print. So the server does not start the module until the operator grants
  the header

## Building and checking it

`make -C extensions check EXT=prusaslicer2-connector` lints the Go half, builds
both halves, installs the release into a demo server over HTTP and over HTTPS
and drives it with `scripts/e2e` the way PrusaSlicer 2 does. `make test` in
this folder runs the Go tests alone.
