// A colour as a page may put it into CSS: #rrggbb or #rrggbbaa, and undefined
// for anything else. The colour can come from a printer, and any other text
// in a style attribute could add a CSS rule of the printer's choice.
export const cssColour = (colorRgba: string | undefined): string | undefined =>
  colorRgba !== undefined && /^#[\da-f]{6}([\da-f]{2})?$/i.test(colorRgba)
    ? colorRgba
    : undefined;

// A colour as a person reads it: #rrggbbaa without its alpha byte when the
// colour is fully opaque, since ff says nothing a reader needs.
export const shownColour = (colorRgba: string): string =>
  colorRgba.replace(/^(#[\da-f]{6})ff$/i, '$1');
