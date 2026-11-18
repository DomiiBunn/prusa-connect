import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { parse } from 'svelte/compiler';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';

// The React Storybook is the design system. `classes.fixture.json` is a copy
// of its class names, taken by scripts/extract-react-classes.mjs while the
// React branch still existed, and this test holds the Svelte components to it.
//
// It compares class names per `data-slot`, and nothing else. An extra wrapper
// element, a different order of children and a gap of one pixel all pass here.
// Opening a component in both Storybooks and looking at it is the second
// check, and it stays a person's job.
//
// A component in the fixture with no folder under src/primitives passes. It is
// not ported yet, and this test is written before most of them exist. A folder
// that is there and does not match fails.

const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = join(here, 'classes.fixture.json');

/** The class names of one React component, by slot. */
type Slots = Record<string, string[]>;
/** Every React component, by name. */
type Fixture = Record<string, Slots>;

/** Which source a class name is on when the two sides disagree. */
type Side = 'react' | 'svelte';

interface Exception {
  /** The component's folder name, which is also its fixture key. */
  component: string;
  /** The `data-slot` name the difference is on. */
  slot: string;
  /** `react` for a class only the fixture has, `svelte` for one only the component has. */
  side: Side;
  classes: string[];
  /** Why the two sides differ here. Every entry needs one. */
  reason: string;
}

// Three kinds of difference are legitimate, and each one gets an entry here
// with its reason:
//
// 1. a part name that comes from bits-ui and has no React counterpart
// 2. markup that the `child` snippet moves to another slot
// 3. a `data-[state=...]` variant that became a scoped `<style>` block. The
//    four state variants tokens.css declares, `state-open` and the rest, need
//    no entry: `stateVariant` below reads both spellings as one name, and a
//    class that only exists to be selected by the component's own `<style>`
//    block is dropped before the comparison.
//
// An entry that matches nothing fails the run, so a dead entry cannot sit
// here unnoticed.
const exceptions: Exception[] = [
  {
    component: 'progress',
    slot: 'progress-indicator',
    side: 'svelte',
    classes: ['bg-muted-foreground/40'],
    reason:
      'A bar with no reading pulses in grey, so it is not read as done. React draws it in the tone.',
  },
  {
    component: 'button',
    slot: 'button',
    side: 'svelte',
    classes: ['text-link'],
    reason:
      "Link text paints --link, which clears 4.5:1 as text. React's --primary measures 3.32:1 on white.",
  },
  {
    component: 'checkbox',
    slot: 'checkbox',
    side: 'react',
    classes: [
      'data-[state=checked]:bg-primary',
      'data-[state=checked]:border-primary',
      'data-[state=checked]:text-primary-foreground',
    ],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary.',
  },
  {
    component: 'checkbox',
    slot: 'checkbox',
    side: 'svelte',
    classes: [
      'data-[state=checked]:bg-control-on',
      'data-[state=checked]:border-control-on',
      'data-[state=checked]:text-control-on-foreground',
      'data-[state=indeterminate]:bg-control-on',
      'data-[state=indeterminate]:border-control-on',
      'data-[state=indeterminate]:text-control-on-foreground',
    ],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary. A header box over rows of which only some are ticked is filled like a ticked one and draws a dash; the React checkbox has no mixed state.',
  },
  {
    component: 'facet-popover',
    slot: 'facet-popover-trigger',
    side: 'react',
    classes: [
      'data-on:[&_svg]:text-primary-foreground',
      'data-on:bg-primary',
      'data-on:hover:bg-primary-hovered',
      'data-on:text-primary-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'facet-popover',
    slot: 'facet-popover-trigger',
    side: 'svelte',
    classes: [
      'data-on:[&_svg]:text-selected-foreground',
      'data-on:bg-selected',
      'data-on:hover:bg-selected-hovered',
      'data-on:text-selected-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'facet-row',
    slot: 'facet-row',
    side: 'react',
    classes: ['data-on:bg-primary', 'data-on:text-primary-foreground'],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'facet-row',
    slot: 'facet-row',
    side: 'svelte',
    classes: ['data-on:bg-selected', 'data-on:text-selected-foreground'],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'field-picker',
    slot: 'field-picker',
    side: 'react',
    classes: ['text-[10px]'],
    reason:
      "The count takes the type scale's smallest step, text-xs, which follows the skin's density. A fixed 10px did not.",
  },
  {
    component: 'notifications',
    slot: 'notifications',
    side: 'react',
    classes: ['text-[0.65rem]'],
    reason:
      "The count keeps the badge's own text-xs, the smallest step of the type scale.",
  },
  {
    component: 'progress',
    slot: 'progress-indicator',
    side: 'react',
    classes: ['bg-primary'],
    reason:
      'Work advancing paints --progress, which the slicer skin gives its own blue.',
  },
  {
    component: 'progress',
    slot: 'progress-indicator',
    side: 'svelte',
    classes: ['bg-progress'],
    reason:
      'Work advancing paints --progress, which the slicer skin gives its own blue.',
  },
  {
    component: 'radio-group',
    slot: 'radio-group-item',
    side: 'react',
    classes: [
      'data-[state=checked]:border-primary',
      'data-[state=checked]:text-primary',
    ],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary.',
  },
  {
    component: 'radio-group',
    slot: 'radio-group-item',
    side: 'svelte',
    classes: [
      'data-[state=checked]:border-control-on',
      'data-[state=checked]:text-control-on',
    ],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary.',
  },
  {
    component: 'row-link',
    slot: 'row-link',
    side: 'svelte',
    classes: [
      'has-[a:focus-visible]:ring-2',
      'has-[a:focus-visible]:ring-inset',
      'has-[a:focus-visible]:ring-ring',
    ],
    reason:
      'A row reached by keyboard shows the focus ring every other control shows. React marked it with a background alone.',
  },
  {
    component: 'sheet',
    slot: 'sheet-overlay',
    side: 'react',
    classes: ['bg-black/60'],
    reason: 'A sheet dims the page as much as a dialog does.',
  },
  {
    component: 'sheet',
    slot: 'sheet-overlay',
    side: 'svelte',
    classes: ['bg-black/50'],
    reason: 'A sheet dims the page as much as a dialog does.',
  },
  {
    component: 'slider',
    slot: 'slider-range',
    side: 'react',
    classes: ['bg-primary'],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary.',
  },
  {
    component: 'slider',
    slot: 'slider-range',
    side: 'svelte',
    classes: ['bg-control-on'],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary.',
  },
  {
    component: 'slider',
    slot: 'slider-thumb',
    side: 'react',
    classes: ['bg-white', 'border-primary'],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary. The thumb takes the page colour, so it is not a white disc in the dark theme.',
  },
  {
    component: 'slider',
    slot: 'slider-thumb',
    side: 'svelte',
    classes: ['bg-background', 'border-control-on'],
    reason:
      'An on control paints --control-on, which a skin can give a colour of its own, as the slicer skin gives its blue. React painted the brand --primary. The thumb takes the page colour, so it is not a white disc in the dark theme.',
  },
  {
    component: 'switch',
    slot: 'switch',
    side: 'react',
    classes: [
      'dark:data-[state=checked]:text-primary-foreground',
      'dark:data-[state=unchecked]:text-foreground',
      'data-[state=checked]:bg-primary',
      'data-[state=unchecked]:bg-input',
      'text-background',
    ],
    reason:
      'On and off must not differ by hue alone. React painted an off track filled with --input and the thumb in the page colour; here off is an outline with an --input thumb, and on is a --control-on track (a skin can give it a colour of its own) with a light thumb that draws a tick.',
  },
  {
    component: 'switch',
    slot: 'switch',
    side: 'svelte',
    classes: [
      'data-[state=checked]:bg-control-on',
      'data-[state=checked]:text-control-on-foreground',
      'data-[state=unchecked]:bg-transparent',
      'data-[state=unchecked]:border-input',
      'data-[state=unchecked]:text-input',
      'disabled:data-[state=unchecked]:border-transparent',
    ],
    reason:
      'On and off must not differ by hue alone. React painted an off track filled with --input and the thumb in the page colour; here off is an outline with an --input thumb, and on is a --control-on track (a skin can give it a colour of its own) with a light thumb that draws a tick.',
  },
  {
    component: 'switch',
    slot: 'switch-thumb',
    side: 'react',
    classes: ['block'],
    reason: 'The thumb is a grid so that the tick sits centred in it.',
  },
  {
    component: 'switch',
    slot: 'switch-thumb',
    side: 'svelte',
    classes: ['grid', 'place-content-center'],
    reason: 'The thumb is a grid so that the tick sits centred in it.',
  },
  {
    component: 'switch',
    slot: 'switch-thumb',
    side: 'svelte',
    classes: [
      'group-data-[size=md]/switch:size-[11px]',
      'group-data-[size=sm]/switch:size-2',
      'group-disabled/switch:text-disabled',
      'text-control-on',
    ],
    reason:
      'An on switch shows the Checkbox tick in its thumb, so the state does not rest on colour alone. React has no tick.',
  },
  {
    component: 'tabs',
    slot: 'tabs-trigger',
    side: 'react',
    classes: ['data-[state=active]:border-primary'],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'tabs',
    slot: 'tabs-trigger',
    side: 'svelte',
    classes: ['data-[state=active]:border-selected'],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'toggle',
    slot: 'toggle',
    side: 'react',
    classes: [
      'data-[state=on]:active:bg-primary-pressed',
      'data-[state=on]:active:text-primary-foreground',
      'data-[state=on]:bg-primary',
      'data-[state=on]:border-primary',
      'data-[state=on]:hover:bg-primary-hovered',
      'data-[state=on]:hover:border-primary-hovered',
      'data-[state=on]:text-primary-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'toggle',
    slot: 'toggle',
    side: 'svelte',
    classes: [
      'data-[state=on]:active:bg-selected-hovered',
      'data-[state=on]:active:text-selected-foreground',
      'data-[state=on]:bg-selected',
      'data-[state=on]:border-selected',
      'data-[state=on]:hover:bg-selected-hovered',
      'data-[state=on]:hover:border-selected-hovered',
      'data-[state=on]:text-selected-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'view-mode',
    slot: 'view-mode',
    side: 'react',
    classes: [
      'aria-checked:bg-primary',
      'aria-checked:border-primary',
      'aria-checked:hover:bg-primary-hovered',
      'aria-checked:hover:border-primary-hovered',
      'aria-checked:text-primary-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'view-mode',
    slot: 'view-mode',
    side: 'svelte',
    classes: [
      'aria-checked:bg-selected',
      'aria-checked:border-selected',
      'aria-checked:hover:bg-selected-hovered',
      'aria-checked:hover:border-selected-hovered',
      'aria-checked:text-selected-foreground',
    ],
    reason:
      'A chosen segment, facet or tab paints --selected, the token for where the person is. React painted the brand --primary.',
  },
  {
    component: 'badge',
    slot: 'badge',
    side: 'svelte',
    classes: ['gap-3', 'pe-4', 'ps-1.5', 'py-1.5', 'tabular-nums', 'text-sm!'],
    reason:
      'Badge takes size="lg" for a pill that holds a picture, and numeric for a reading in tabular digits. React has neither.',
  },
  {
    component: 'badge',
    slot: 'badge',
    side: 'svelte',
    classes: [
      '[button&]:cursor-pointer',
      '[button&]:hover:bg-accent',
      '[button&]:hover:text-foreground',
    ],
    reason:
      'A badge drawn as a button through `child` shows a pointer and takes the outline hover fill. React drew only the anchor form.',
  },
  {
    component: 'button',
    slot: 'button',
    side: 'svelte',
    classes: ['gap-1.5', 'max-w-full', 'text-start'],
    reason:
      'A link button takes size="inline-icon", a flex row with a gap for a link that carries an icon. React has no link size.',
  },
  {
    component: 'button',
    slot: 'button',
    side: 'svelte',
    classes: ['active:border-transparent'],
    reason:
      'A pressed primary button drops its border, as the Prusa Design System button does. React kept the border on press.',
  },
  {
    component: 'card',
    slot: 'card',
    side: 'svelte',
    classes: [
      'bg-destructive/5',
      'bg-warning/5',
      'border-destructive',
      'border-warning',
      'gap-0',
      'gap-1',
      'gap-3',
      'py-0',
      'py-2',
    ],
    reason:
      'Card takes tone="critical" or "attention" to tint a printer card by severity, and density="compact", "tight" or "flush" for the padding of a dense one. React has neither.',
  },
  {
    component: 'collapsible',
    slot: 'collapsible-trigger',
    side: 'svelte',
    classes: ['hover:text-warning-text', 'text-warning-text'],
    reason:
      'CollapsibleTrigger takes tone="warning" to draw the row in the warning text colour. React has no tone.',
  },
  {
    component: 'label',
    slot: 'label',
    side: 'svelte',
    classes: [
      'font-normal',
      'items-start',
      'leading-snug',
      'text-muted-foreground',
      'text-xs',
    ],
    reason:
      'Label takes variant="option" (beside a checkbox or radio) and "hint" (small, muted). React has one look.',
  },
  {
    component: 'dialog',
    slot: 'dialog-content',
    side: 'react',
    classes: ['sm:max-w-lg'],
    reason:
      'A dialog takes a size of md, lg or xl, and the width comes from a table of the three. The React width, sm:max-w-lg, is the md entry of that table, which this comparison does not read.',
  },
  {
    component: 'dialog',
    slot: 'dialog-content',
    side: 'svelte',
    classes: ['lg', 'md', 'xl'],
    reason:
      'The keys of the width table, which the class scan reads as class names. They are not classes and never reach the markup.',
  },
  {
    component: 'select',
    slot: 'select-trigger',
    side: 'svelte',
    classes: ['default', 'info'],
    reason:
      'The keys of the tone table, which the class scan reads as class names. They are not classes and never reach the markup.',
  },
  {
    component: 'material-swatch',
    slot: 'material-swatch',
    side: 'react',
    classes: [
      'cursor-pointer',
      'focus-visible:ring-2',
      'focus-visible:ring-offset-2',
      'focus-visible:ring-offset-background',
      'focus-visible:ring-ring',
      'h-auto',
      'max-w-[min(20rem,var(--radix-popper-available-width))]',
      'max-w-full',
      'mt-2',
      'object-contain',
      'object-cover',
      'outline-none',
      'text-muted-foreground',
      'text-xs',
      'w-auto',
    ],
    reason:
      'The swatch draws a colour and no photo. These classes drew the photo and its larger preview, and no screen shows a photo.',
  },
  {
    component: 'tooltip',
    slot: 'tooltip-content',
    side: 'react',
    classes: [
      'fill-foreground',
      'rotate-45',
      'size-2.5',
      'translate-y-[calc(-50%_-_2px)]',
    ],
    reason:
      "React draws the tip as a square turned on its corner. bits-ui draws its own triangle and orients it with an inline transform-origin written for that shape, which throws a rotated square off the bubble, and an inline style cannot be overridden from a class without !important. The triangle is bits-ui's, so none of these four names is here.",
  },
  {
    component: 'tooltip',
    slot: 'tooltip-content',
    side: 'svelte',
    classes: ['text-foreground'],
    reason:
      "bits-ui's triangle is an svg filled with currentColor, so the tip takes its colour from the text colour rather than from a fill utility.",
  },
  {
    component: 'value-editor',
    slot: 'value-editor',
    side: 'svelte',
    classes: ['flex-row', 'gap-1', 'items-center', 'justify-start', 'w-full'],
    reason:
      'A tool changer opens the editor on a list of its toolheads, with a way back from the picked one in the header. The React editor sets one value and has neither.',
  },
  {
    component: 'confirm-dialog',
    slot: 'dialog-body',
    side: 'svelte',
    classes: ['empty:hidden', 'flex', 'flex-col', 'gap-4'],
    reason:
      "What a caller draws between the description and the buttons scrolls in a body of its own, so a long one keeps the title and the buttons in view. The React confirm dialog put the caller's markup straight into the panel and had no body.",
  },
  {
    component: 'empty-state',
    slot: 'empty-state',
    side: 'svelte',
    classes: [
      'flex-1',
      'font-medium',
      'gap-1',
      'justify-center',
      'p-3',
      'size-6',
      'text-xs',
    ],
    reason:
      'A dashboard card needs an empty state that fills a small tile, the sm size. React has one size, the page-sized one.',
  },
  {
    component: 'collapsible',
    slot: 'collapsible-trigger',
    side: 'svelte',
    classes: [
      'border',
      'border-border',
      'group-data-[state=open]/collapsible-trigger:rotate-90',
      'hover:bg-accent',
      'justify-start',
      'min-h-11',
      'motion-reduce:transition-none',
      'px-1',
      'px-3',
      'py-2',
      'rounded-sm',
      'text-start',
      'transition-transform',
    ],
    reason:
      'A disclosure that opens a section of a settings page or a guide is a bordered row or a bare row with a chevron that turns when open, the row and inline variants. React drew only the bare heading and left the chevron to each caller.',
  },
  {
    component: 'table',
    slot: 'sortable-table-head',
    side: 'svelte',
    classes: [
      'flex-row-reverse',
      'focus-visible:ring-2',
      'focus-visible:ring-offset-2',
      'focus-visible:ring-offset-background',
      'focus-visible:ring-ring',
      'gap-1',
      'hover:text-foreground',
      'inline-flex',
      'items-center',
      'opacity-50',
      'outline-none',
      'rounded-sm',
      'size-3',
      'text-end',
    ],
    reason:
      'A column a listing sorts by takes a header button with the direction arrow. React sorted no table, so the slot has no counterpart.',
  },
  {
    component: 'copy-button',
    slot: 'copy-button',
    side: 'react',
    classes: ['animate-spin'],
    reason:
      "The pending glyph is the Spinner primitive, and its rotation sits on the spinner's own slot.",
  },
  {
    component: 'code-block',
    slot: 'code-block',
    side: 'react',
    classes: ['animate-spin'],
    reason:
      "The pending glyph is the Spinner primitive, and its rotation sits on the spinner's own slot.",
  },
  {
    component: 'sonner',
    slot: 'sonner',
    side: 'react',
    classes: ['animate-spin'],
    reason:
      "The pending glyph is the Spinner primitive, and its rotation sits on the spinner's own slot.",
  },
  {
    component: 'button',
    slot: 'button',
    side: 'svelte',
    classes: [
      'aria-disabled:bg-disabled',
      'aria-disabled:border-transparent',
      'aria-disabled:text-disabled-foreground',
    ],
    reason:
      'A button that keeps focus while its action runs is marked aria-disabled instead of disabled, and it draws the same disabled look. React styled only the disabled attribute.',
  },
  {
    component: 'button',
    slot: 'button',
    side: 'svelte',
    classes: [
      'h-auto',
      'hover:underline',
      'inline',
      'p-0',
      'text-primary',
      'underline-offset-4',
      'whitespace-normal',
    ],
    reason:
      'The link variant draws an action as a link inside running text, with no box and no height. React had no link variant.',
  },
  {
    component: 'badge',
    slot: 'badge',
    side: 'svelte',
    classes: ['font-mono', 'leading-none', 'tracking-wide', 'uppercase'],
    reason:
      'A printer state reads as a short code in capitals, the mono look the state variants carry. React badges had one face.',
  },
  {
    component: 'alert-dialog',
    slot: 'alert-dialog-title',
    side: 'svelte',
    classes: ['wrap-anywhere'],
    reason:
      'A delete names the file, and a file name with no space in it would push the title and the buttons past the panel. React has no guard for it.',
  },
  {
    component: 'alert-dialog',
    slot: 'alert-dialog-description',
    side: 'svelte',
    classes: ['wrap-anywhere'],
    reason:
      'A description can name a file too, and a file name with no space in it would push the text past the panel. React has no guard for it.',
  },
  {
    component: 'badge',
    slot: 'badge',
    side: 'svelte',
    classes: ['py-1'],
    reason:
      "The md size gives a state badge the height of a row's other controls. React badges had one size.",
  },
];

// Every component in the fixture is ported, so the comparison skips nothing.
// A name here whose folder is gone fails the run, the same way a dead
// exception does.
const unported: string[] = [];

// React writes the open and shut states as a data attribute selector. Here
// they are custom variants declared in src/styles/tokens.css. Both spellings
// read as one name.
const stateVariant = /data-\[state=(open|closed|delayed-open|instant-open)\]:/g;
const normalise = (name: string): string =>
  name.replace(stateVariant, 'state-$1:');

const classNames = (text: string): string[] =>
  text.split(/\s+/).filter(Boolean);

type Node = Record<string, unknown>;
const isNode = (value: unknown): value is Node =>
  typeof value === 'object' && value !== null;

/**
 * Every class name under a TypeScript node.
 *
 * A Svelte `variants.ts` is a flat `Record<Name, string>` of class strings, so
 * every string in it is classes. A quoted key such as `'icon-sm'` is not, and
 * it is the one string this skips.
 */
const literalsUnder = (node: ts.Node): string[] => {
  const found: string[] = [];
  const visit = (inner: ts.Node): void => {
    if (
      ts.isStringLiteral(inner) ||
      ts.isNoSubstitutionTemplateLiteral(inner)
    ) {
      found.push(...classNames(inner.text));
      return;
    }
    if (ts.isPropertyAssignment(inner)) {
      visit(inner.initializer);
      return;
    }
    ts.forEachChild(inner, visit);
  };
  visit(node);
  return found;
};

/** The class names each `const` of a `.ts` module holds, by name. */
const moduleClasses = (path: string): Map<string, string[]> => {
  const source = ts.createSourceFile(
    path,
    readFileSync(path, 'utf8'),
    ts.ScriptTarget.Latest,
    true,
  );
  const found = new Map<string, string[]>();
  for (const statement of source.statements) {
    if (!ts.isVariableStatement(statement)) continue;
    for (const declaration of statement.declarationList.declarations) {
      if (!ts.isIdentifier(declaration.name) || !declaration.initializer)
        continue;
      found.set(declaration.name.text, literalsUnder(declaration.initializer));
    }
  }
  return found;
};

interface Scope {
  /** A name imported from a module in the component's own folder. */
  imported: Map<string, string[]>;
  /** A `const` declared in the component's own `<script>`. */
  local: Map<string, unknown>;
}

const joiners = ['cn', 'clsx', 'classNames', 'twMerge'];

/** Every class name an expression in a Svelte `class` attribute can produce. */
const expressionClasses = (
  node: unknown,
  scope: Scope,
  seen: Set<string>,
): string[] => {
  if (!isNode(node)) return [];
  const parts = (list: unknown[]): string[] =>
    list.flatMap((item) => expressionClasses(item, scope, seen));

  switch (node.type) {
    case 'Literal':
      return typeof node.value === 'string' ? classNames(node.value) : [];
    case 'TemplateLiteral': {
      const quasis = Array.isArray(node.quasis) ? node.quasis : [];
      return quasis.flatMap((quasi) => {
        if (!isNode(quasi) || !isNode(quasi.value)) return [];
        const cooked = quasi.value.cooked;
        return typeof cooked === 'string' ? classNames(cooked) : [];
      });
    }
    case 'ArrayExpression':
      return Array.isArray(node.elements) ? parts(node.elements) : [];
    case 'ConditionalExpression':
      return parts([node.consequent, node.alternate]);
    // `loading && 'animate-spin'` puts the class on the right. The left is a
    // condition, and reading it turns `size === 'lg'` into a class called `lg`.
    case 'LogicalExpression':
      return node.operator === '&&'
        ? expressionClasses(node.right, scope, seen)
        : parts([node.left, node.right]);
    // Svelte's object form, `class={{ active: open }}`. The keys are classes.
    case 'ObjectExpression': {
      const properties = Array.isArray(node.properties) ? node.properties : [];
      return properties.flatMap((property) => {
        if (!isNode(property) || !isNode(property.key)) return [];
        const key = property.key;
        if (typeof key.name === 'string') return classNames(key.name);
        return typeof key.value === 'string' ? classNames(key.value) : [];
      });
    }
    case 'CallExpression': {
      const callee = isNode(node.callee) ? node.callee : undefined;
      const name = typeof callee?.name === 'string' ? callee.name : '';
      const args = Array.isArray(node.arguments) ? node.arguments : [];
      return joiners.includes(name)
        ? parts(args)
        : expressionClasses(callee, scope, seen);
    }
    case 'Identifier': {
      const name = typeof node.name === 'string' ? node.name : '';
      if (seen.has(name)) return [];
      seen.add(name);
      const imported = scope.imported.get(name);
      if (imported) return imported;
      return expressionClasses(scope.local.get(name), scope, seen);
    }
    // `variants[variant]` and `recipe.base` reach every value of the record.
    case 'MemberExpression':
      return expressionClasses(node.object, scope, seen);
    default:
      return [];
  }
};

/** Every `class` selector the component's own `<style>` block declares. */
const styledClasses = (css: unknown): Set<string> => {
  const found = new Set<string>();
  const visit = (node: unknown): void => {
    if (Array.isArray(node)) {
      for (const item of node) visit(item);
      return;
    }
    if (!isNode(node)) return;
    if (node.type === 'ClassSelector' && typeof node.name === 'string') {
      found.add(node.name);
    }
    for (const value of Object.values(node)) visit(value);
  };
  visit(css);
  return found;
};

/** The text of an attribute whose value is plain text, such as `data-slot="checkbox"`. */
const textAttribute = (
  attributes: unknown[],
  name: string,
): string | undefined => {
  for (const attribute of attributes) {
    if (!isNode(attribute)) continue;
    if (attribute.type !== 'Attribute' || attribute.name !== name) continue;
    if (!Array.isArray(attribute.value)) continue;
    const first: unknown = attribute.value[0];
    if (
      isNode(first) &&
      first.type === 'Text' &&
      typeof first.data === 'string'
    ) {
      return first.data;
    }
  }
  return undefined;
};

/**
 * The scope a Svelte component's `class` expressions are read in: what its
 * `<script>` declares, and what it imports from its own folder.
 *
 * An import from another folder is not followed. `row-link` and
 * `control-trigger` have their own fixture entries, and the React extractor
 * follows no import either.
 */
const scopeOf = (script: unknown, folder: string): Scope => {
  const scope: Scope = { imported: new Map(), local: new Map() };
  const body =
    isNode(script) && isNode(script.content) ? script.content.body : undefined;
  if (!Array.isArray(body)) return scope;

  for (const statement of body) {
    if (!isNode(statement)) continue;
    if (
      statement.type === 'VariableDeclaration' &&
      Array.isArray(statement.declarations)
    ) {
      for (const declaration of statement.declarations) {
        if (!isNode(declaration) || !isNode(declaration.id)) continue;
        if (typeof declaration.id.name === 'string') {
          scope.local.set(declaration.id.name, declaration.init);
        }
      }
      continue;
    }
    if (statement.type !== 'ImportDeclaration') continue;
    const source = isNode(statement.source)
      ? statement.source.value
      : undefined;
    if (typeof source !== 'string' || !source.startsWith('./')) continue;
    const path = join(folder, `${source.slice(2)}.ts`);
    if (!existsSync(path)) continue;
    const exported = moduleClasses(path);
    const specifiers = Array.isArray(statement.specifiers)
      ? statement.specifiers
      : [];
    for (const specifier of specifiers) {
      if (
        !isNode(specifier) ||
        !isNode(specifier.imported) ||
        !isNode(specifier.local)
      ) {
        continue;
      }
      const from = specifier.imported.name;
      const to = specifier.local.name;
      if (typeof from !== 'string' || typeof to !== 'string') continue;
      scope.imported.set(to, exported.get(from) ?? []);
    }
  }
  return scope;
};

/**
 * The class names of one Svelte component, by slot.
 *
 * React writes every part of a component in one file. Here a part is its own
 * `.svelte` file in the component's folder, so every one of them is read and
 * the slots are put together.
 *
 * A class on an element with no `data-slot` of its own goes to the nearest
 * ancestor that has one, and a file with no `data-slot` anywhere records one
 * slot named after the file. Those are the two rules the React extractor reads
 * its own tree by.
 */
const svelteSlots = (folder: string, component: string): Slots | undefined => {
  if (!existsSync(folder)) return undefined;
  const entries = readdirSync(folder);
  const parts = entries.filter(
    (name) => name.endsWith('.svelte') && !name.endsWith('.stories.svelte'),
  );
  const found = new Map<string, Set<string>>();

  if (parts.length === 0) {
    // `control-trigger` and `row-link` are class recipes with no markup.
    const modules = entries.filter(
      (name) =>
        name.endsWith('.ts') &&
        !name.endsWith('.test.ts') &&
        name !== 'types.ts',
    );
    if (modules.length === 0) return undefined;
    const classes = modules.flatMap((name) => [
      ...moduleClasses(join(folder, name)).values(),
    ]);
    found.set(component, new Set(classes.flat()));
  }

  for (const part of parts) {
    readPart(join(folder, part), folder, part.replace('.svelte', ''), found);
  }

  const slots: Slots = {};
  for (const [slot, classes] of [...found].sort(([left], [right]) =>
    left.localeCompare(right),
  )) {
    slots[slot] = [...classes].sort();
  }
  return slots;
};

/** Reads one `.svelte` file into the slot map the whole folder shares. */
const readPart = (
  file: string,
  folder: string,
  fallback: string,
  found: Map<string, Set<string>>,
): void => {
  const ast = parse(readFileSync(file, 'utf8'), { modern: true });
  const tree = ast as unknown as Node;
  const scope = scopeOf(tree.instance, folder);
  const hooks = styledClasses(tree.css);

  const walk = (node: unknown, slot: string): void => {
    if (Array.isArray(node)) {
      for (const item of node) walk(item, slot);
      return;
    }
    if (!isNode(node)) return;
    let inner = slot;
    const attributes = Array.isArray(node.attributes)
      ? node.attributes
      : undefined;
    if (attributes) {
      inner = textAttribute(attributes, 'data-slot') ?? slot;
      for (const attribute of attributes) {
        if (!isNode(attribute)) continue;
        if (attribute.type !== 'Attribute' || attribute.name !== 'class')
          continue;
        const value = attribute.value;
        const nodes = Array.isArray(value) ? value : [value];
        const classes = nodes.flatMap((part) => {
          if (!isNode(part)) return [];
          if (part.type === 'Text') {
            return typeof part.data === 'string' ? classNames(part.data) : [];
          }
          return expressionClasses(part.expression, scope, new Set());
        });
        const into = found.get(inner) ?? new Set<string>();
        for (const name of classes) if (!hooks.has(name)) into.add(name);
        found.set(inner, into);
      }
    }
    for (const [key, value] of Object.entries(node)) {
      if (key === 'attributes') continue;
      walk(value, inner);
    }
  };
  walk(tree.fragment, fallback);
};

interface Difference {
  component: string;
  slot: string;
  side: Side;
  classes: string[];
}

/** The class names on one side and not the other, for every component that is ported. */
const differences = (
  fixture: Fixture,
  root: string,
  allowed: Exception[],
  skipped: string[] = [],
): {
  report: string;
  dead: Exception[];
  staleSkips: string[];
} => {
  const used = new Set<Exception>();
  const found: Difference[] = [];

  const only = (
    component: string,
    slot: string,
    side: Side,
    mine: Set<string>,
    theirs: Set<string>,
  ): void => {
    const missing = [...mine].filter((name) => !theirs.has(name)).sort();
    const left = missing.filter((name) => {
      const excused = allowed.find(
        (entry) =>
          entry.component === component &&
          entry.slot === slot &&
          entry.side === side &&
          entry.classes.includes(name),
      );
      if (excused) used.add(excused);
      return !excused;
    });
    if (left.length > 0) found.push({ component, slot, side, classes: left });
  };

  for (const [component, reactSlots] of Object.entries(fixture)) {
    if (skipped.includes(component)) continue;
    const ported = svelteSlots(join(root, component), component);
    if (!ported) continue;
    const slots = new Set([...Object.keys(reactSlots), ...Object.keys(ported)]);
    for (const slot of [...slots].sort()) {
      const react = new Set((reactSlots[slot] ?? []).map(normalise));
      const svelte = new Set((ported[slot] ?? []).map(normalise));
      only(component, slot, 'react', react, svelte);
      only(component, slot, 'svelte', svelte, react);
    }
  }

  const lines = found.map(
    ({ component, slot, side, classes }) =>
      `${component} / ${slot}: only on the ${side} side: ${classes.join(' ')}`,
  );
  return {
    report: lines.join('\n'),
    dead: allowed.filter((entry) => !used.has(entry)),
    staleSkips: skipped.filter((name) => !existsSync(join(root, name))),
  };
};

describe('the Svelte components carry the React class names', () => {
  const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as Fixture;

  it('reads the fixture', () => {
    expect(Object.keys(fixture).length).toBe(65);
    expect(fixture.button?.button).toContain('rounded-sm');
  });

  it('matches every component that is ported', () => {
    const { report } = differences(fixture, here, exceptions, unported);
    expect(report).toBe('');
  });

  it('has no exception that matches nothing', () => {
    const { dead } = differences(fixture, here, exceptions, unported);
    expect(dead.map((entry) => `${entry.component} / ${entry.slot}`)).toEqual(
      [],
    );
  });

  it('has no name on the unported list whose folder is gone', () => {
    const { staleSkips } = differences(fixture, here, exceptions, unported);
    expect(staleSkips).toEqual([]);
  });
});

describe('the comparison itself', () => {
  const write = (files: Record<string, string>): string => {
    const root = mkdtempSync(join(tmpdir(), 'classes-'));
    for (const [path, text] of Object.entries(files)) {
      const file = join(root, path);
      mkdirSync(dirname(file), { recursive: true });
      writeFileSync(file, text);
    }
    return root;
  };

  const fixture: Fixture = { widget: { widget: ['flex', 'rounded-sm'] } };

  it('passes a component that is not ported yet', () => {
    const { report } = differences(fixture, write({}), []);
    expect(report).toBe('');
  });

  it('names a class the Svelte side is missing', () => {
    const root = write({
      'widget/widget.svelte': '<div data-slot="widget" class="flex"></div>',
    });
    expect(differences(fixture, root, []).report).toBe(
      'widget / widget: only on the react side: rounded-sm',
    );
  });

  it('names a class only the Svelte side has', () => {
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="flex rounded-sm gap-2"></div>',
    });
    expect(differences(fixture, root, []).report).toBe(
      'widget / widget: only on the svelte side: gap-2',
    );
  });

  it('reads a class out of the folder’s own variants module', () => {
    const root = write({
      'widget/variants.ts': "export const base = 'flex rounded-sm';",
      'widget/widget.svelte':
        '<script lang="ts">\n  import { base } from \'./variants\';\n</script>\n\n<div data-slot="widget" class={base}></div>',
    });
    expect(differences(fixture, root, []).report).toBe('');
  });

  it('reads every value of a variant record, not the one a story picks', () => {
    const many: Fixture = {
      widget: { widget: ['bg-primary', 'bg-secondary'] },
    };
    const root = write({
      'widget/variants.ts':
        "export const tones = { primary: 'bg-primary', secondary: 'bg-secondary' };",
      'widget/widget.svelte':
        '<script lang="ts">\n  import { tones } from \'./variants\';\n  let { tone = \'primary\' } = $props();\n</script>\n\n<div data-slot="widget" class={tones[tone]}></div>',
    });
    expect(differences(many, root, []).report).toBe('');
  });

  it('reads the React and the Svelte spelling of an open state as one name', () => {
    // The class name is written in two pieces. The lint rule that refuses a
    // Tailwind arbitrary value cannot tell one from a React class name quoted
    // in a test.
    const reactSpelling = 'data-' + '[state=open]:flex';
    const stateful: Fixture = { widget: { widget: [reactSpelling] } };
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="state-open:flex"></div>',
    });
    expect(differences(stateful, root, []).report).toBe('');
  });

  it('drops a class the component’s own style block selects', () => {
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="box flex rounded-sm"></div>\n\n<style>\n  .box {\n    color: red;\n  }\n</style>',
    });
    expect(differences(fixture, root, []).report).toBe('');
  });

  it('puts a class with no slot of its own on the nearest slot above it', () => {
    const nested: Fixture = {
      widget: { widget: ['flex', 'rounded-sm', 'gap-2'] },
    };
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="flex rounded-sm"><span class="gap-2"></span></div>',
    });
    expect(differences(nested, root, []).report).toBe('');
  });

  it('accepts a difference an exception excuses, and reports the rest', () => {
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="flex gap-2"></div>',
    });
    const allowed: Exception[] = [
      {
        component: 'widget',
        slot: 'widget',
        side: 'svelte',
        classes: ['gap-2'],
        reason: 'the test for the test needs one entry that matches',
      },
    ];
    const { report, dead } = differences(fixture, root, allowed);
    expect(report).toBe('widget / widget: only on the react side: rounded-sm');
    expect(dead).toEqual([]);
  });

  it('reports an exception that matches nothing', () => {
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="flex rounded-sm"></div>',
    });
    const allowed: Exception[] = [
      {
        component: 'widget',
        slot: 'widget',
        side: 'svelte',
        classes: ['gap-2'],
        reason: 'nothing here carries gap-2 any more',
      },
    ];
    expect(differences(fixture, root, allowed).dead).toEqual(allowed);
  });

  it('skips a component the unported list names, and reports a name with no folder', () => {
    const root = write({
      'widget/widget.svelte': '<div data-slot="widget" class="grid"></div>',
    });
    expect(differences(fixture, root, [], ['widget']).report).toBe('');
    expect(differences(fixture, root, [], ['widget']).staleSkips).toEqual([]);
    expect(differences(fixture, root, [], ['gone']).staleSkips).toEqual([
      'gone',
    ]);
  });

  it('reads every part file of a folder, each under its own slot', () => {
    const split: Fixture = {
      widget: { widget: ['flex'], 'widget-mark': ['gap-2'] },
    };
    const root = write({
      'widget/widget.svelte': '<div data-slot="widget" class="flex"></div>',
      'widget/widget-mark.svelte': '<span class="gap-2"></span>',
    });
    expect(differences(split, root, []).report).toBe('');
  });

  it('reads a class recipe folder that has no markup', () => {
    const recipe: Fixture = {
      'row-link': { 'row-link': ['relative', 'z-10'] },
    };
    const root = write({
      'row-link/variants.ts': "export const above = 'relative z-10';",
    });
    expect(differences(recipe, root, []).report).toBe('');
  });

  it('reports every offender, not the first', () => {
    const two: Fixture = {
      widget: { widget: ['flex'], 'widget-mark': ['gap-2'] },
    };
    const root = write({
      'widget/widget.svelte':
        '<div data-slot="widget" class="grid"><span data-slot="widget-mark" class="gap-4"></span></div>',
    });
    expect(differences(two, root, []).report.split('\n')).toEqual([
      'widget / widget: only on the react side: flex',
      'widget / widget: only on the svelte side: grid',
      'widget / widget-mark: only on the react side: gap-2',
      'widget / widget-mark: only on the svelte side: gap-4',
    ]);
  });
});
