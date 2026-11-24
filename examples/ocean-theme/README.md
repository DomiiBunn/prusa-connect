# Ocean theme

An example extension theme. It is an asset module whose manifest offers one
stylesheet under `ui.themes` and runs no script. Once it is installed and on,
pick it under Settings → Appearance → Extension theme, or make it the server's
default with `core.frontend.extension_theme: <namespace>/ocean`.

`make demo-modules` builds a release of it beside the other examples, under
`bin/demo-modules/ocean-theme`. Run `prusa-connect module add` on that
directory to install it.

A theme's stylesheet is linked after the app's own, so it can override any
rule. Class names and layout are not a stable interface, so a theme that
reaches past the tokens may break when the app changes.
