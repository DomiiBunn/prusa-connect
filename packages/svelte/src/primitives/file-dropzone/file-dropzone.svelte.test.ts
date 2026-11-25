import { flushSync } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';

import { expectNoViolations } from '../../test/axe';
import FileDropzone from './file-dropzone.svelte';

const gcode = (name: string): File =>
  new File(['G1 X10'], name, { type: 'text/x-gcode' });

/**
 * A native drag event carrying real files.
 *
 * A plain object fails the `DragEvent` constructor's type check, and copying a
 * `dataTransfer` property onto a synthetic event misses
 * `DataTransfer.prototype.files`, which is an inherited getter. Dispatching
 * the native event avoids both.
 */
const drag = (
  target: Element,
  type: 'dragenter' | 'dragover' | 'dragleave' | 'drop',
  files: File[] = [],
): void => {
  const dataTransfer = new DataTransfer();
  for (const file of files) dataTransfer.items.add(file);
  target.dispatchEvent(
    new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer }),
  );
};

/** Every batch the zone reported, by file name, so nothing is read off an index. */
const namesOf = (batches: File[][]): string[][] =>
  batches.map((batch) => batch.map((file) => file.name));

const zoneOf = (container: HTMLElement): HTMLElement => {
  const zone = container.querySelector<HTMLElement>(
    '[data-slot="file-dropzone"]',
  );
  if (zone === null) throw new Error('no dropzone rendered');
  return zone;
};

describe('FileDropzone', () => {
  it('highlights the zone while a drag is over it, and clears on the way out', async () => {
    const { container } = await render(FileDropzone, {
      props: { onFiles: vi.fn() },
    });
    const zone = zoneOf(container);

    drag(zone, 'dragover');
    await expect
      .element(page.elementLocator(zone))
      .toHaveAttribute('data-state', 'dragging-over');

    drag(zone, 'dragleave');
    await expect
      .element(page.elementLocator(zone))
      .toHaveAttribute('data-state', 'idle');
  });

  it('reports the files that fit and names the ones that do not', async () => {
    const batches: File[][] = [];
    const { container } = await render(FileDropzone, {
      props: {
        onFiles: (files: File[]) => batches.push(files),
        accept: '.gcode,.bgcode',
      },
    });
    const zone = zoneOf(container);

    drag(zone, 'drop', [
      gcode('benchy.gcode'),
      new File(['x'], 'firmware.bgcode', { type: 'application/octet-stream' }),
      new File(['notes'], 'notes.txt', { type: 'text/plain' }),
    ]);

    expect(namesOf(batches)).toEqual([['benchy.gcode', 'firmware.bgcode']]);

    await expect
      .element(page.getByRole('alert'))
      .toHaveTextContent(
        'notes.txt was not accepted. Expected .gcode, .bgcode',
      );
    await expect
      .element(page.elementLocator(zone))
      .toHaveAttribute('data-state', 'rejected');
  });

  it('matches a media type pattern as well as an extension', async () => {
    const batches: File[][] = [];
    const { container } = await render(FileDropzone, {
      props: {
        onFiles: (files: File[]) => batches.push(files),
        accept: 'image/*',
      },
    });

    drag(zoneOf(container), 'drop', [
      new File(['x'], 'plate.png', { type: 'image/png' }),
      new File(['x'], 'plate.stl', { type: 'model/stl' }),
    ]);

    expect(namesOf(batches)).toEqual([['plate.png']]);
  });

  it('takes the drop nowhere while it is disabled', async () => {
    const batches: File[][] = [];
    const { container } = await render(FileDropzone, {
      props: {
        onFiles: (files: File[]) => batches.push(files),
        disabled: true,
      },
    });
    const zone = zoneOf(container);

    drag(zone, 'dragover');
    drag(zone, 'drop', [gcode('benchy.gcode')]);

    expect(batches).toEqual([]);
    await expect
      .element(page.elementLocator(zone))
      .toHaveAttribute('data-state', 'disabled');
  });

  it('reaches the file input from the keyboard', async () => {
    const { container } = await render(FileDropzone, {
      props: { onFiles: vi.fn() },
    });
    const input =
      container.querySelector<HTMLInputElement>('input[type="file"]');
    if (input === null) throw new Error('no file input rendered');
    const opened = vi.spyOn(input, 'click');

    const button = page.getByRole('button', { name: 'Choose files' });
    await userEvent.tab();
    await expect.element(button).toHaveFocus();

    await userEvent.keyboard('{Enter}');
    expect(opened).toHaveBeenCalledTimes(1);
  });

  it('reports a whole batch from one pick', async () => {
    const batches: File[][] = [];
    const { container } = await render(FileDropzone, {
      props: {
        onFiles: (files: File[]) => batches.push(files),
        accept: '.gcode',
      },
    });
    const input =
      container.querySelector<HTMLInputElement>('input[type="file"]');
    if (input === null) throw new Error('no file input rendered');

    await page
      .elementLocator(input)
      .upload([gcode('a.gcode'), gcode('b.gcode')]);

    expect(namesOf(batches)).toEqual([['a.gcode', 'b.gcode']]);
    // Cleared, so picking the same file again still fires a change event.
    expect(input.value).toBe('');
  });

  it('lays the compact zone out as a row and drops the formats line', async () => {
    const { container } = await render(FileDropzone, {
      props: { onFiles: vi.fn(), accept: '.gcode,.bgcode', compact: true },
    });
    const zone = zoneOf(container);

    expect(zone.className).toContain('flex-wrap');
    expect(zone.className).not.toContain('flex-col');
    expect(container.textContent).not.toContain('Accepted:');
    // The line naming the formats goes, so the label has to carry them.
    await expect
      .element(page.getByLabelText('Upload files (.gcode, .bgcode)'))
      .toBeInTheDocument();
    await expectNoViolations(container);
  });

  it('lays the panel out as a column and names the accepted formats', async () => {
    const { container } = await render(FileDropzone, {
      props: { onFiles: vi.fn(), accept: '.gcode,.bgcode' },
    });
    const zone = zoneOf(container);

    expect(zone.className).toContain('flex-col');
    expect(zone.className).not.toContain('flex-wrap');
    expect(container.textContent).toContain('Accepted: .gcode, .bgcode');
    await expectNoViolations(container);
  });

  describe('over the whole page', () => {
    /** Somewhere on the page that is not the zone. */
    const elsewhere = (): HTMLElement => {
      const spot = document.createElement('div');
      document.body.append(spot);
      return spot;
    };

    const sheet = (): Element | null =>
      document.querySelector('[data-slot="file-dropzone-page"]');

    it('takes a file dropped outside the zone, and covers the page while it is held', async () => {
      const batches: File[][] = [];
      await render(FileDropzone, {
        props: {
          onFiles: (files: File[]) => batches.push(files),
          accept: '.gcode',
          page: true,
        },
      });
      const spot = elsewhere();

      drag(spot, 'dragenter', [gcode('benchy.gcode')]);
      await expect.poll(sheet).not.toBeNull();

      drag(spot, 'drop', [gcode('benchy.gcode')]);
      expect(namesOf(batches)).toEqual([['benchy.gcode']]);
      await expect.poll(sheet).toBeNull();
      spot.remove();
    });

    it('lifts the sheet once the drag has left every element it entered', async () => {
      await render(FileDropzone, { props: { onFiles: vi.fn(), page: true } });
      const outer = elsewhere();
      const inner = document.createElement('span');
      outer.append(inner);

      drag(outer, 'dragenter', [gcode('a.gcode')]);
      drag(inner, 'dragenter', [gcode('a.gcode')]);
      drag(outer, 'dragleave', [gcode('a.gcode')]);
      await expect.poll(sheet).not.toBeNull();

      drag(inner, 'dragleave', [gcode('a.gcode')]);
      await expect.poll(sheet).toBeNull();
      outer.remove();
    });

    it('reports a drop on the zone itself once', async () => {
      const batches: File[][] = [];
      const { container } = await render(FileDropzone, {
        props: { onFiles: (files: File[]) => batches.push(files), page: true },
      });

      drag(zoneOf(container), 'drop', [gcode('benchy.gcode')]);

      expect(namesOf(batches)).toEqual([['benchy.gcode']]);
    });

    it('leaves a drop outside alone without `page`', async () => {
      const onFiles = vi.fn();
      await render(FileDropzone, { props: { onFiles } });
      const spot = elsewhere();

      drag(spot, 'dragenter', [gcode('benchy.gcode')]);
      drag(spot, 'drop', [gcode('benchy.gcode')]);

      expect(onFiles).not.toHaveBeenCalled();
      expect(sheet()).toBeNull();
      spot.remove();
    });

    it('takes nothing and draws no sheet while it is disabled', async () => {
      const onFiles = vi.fn();
      await render(FileDropzone, {
        props: { onFiles, page: true, disabled: true },
      });
      const spot = elsewhere();

      drag(spot, 'dragenter', [gcode('benchy.gcode')]);
      flushSync();
      expect(sheet()).toBeNull();
      drag(spot, 'drop', [gcode('benchy.gcode')]);

      expect(onFiles).not.toHaveBeenCalled();
      spot.remove();
    });

    it('takes nothing and draws no sheet while a dialog is open', async () => {
      const onFiles = vi.fn();
      await render(FileDropzone, { props: { onFiles, page: true } });
      const dialog = elsewhere();
      dialog.setAttribute('aria-modal', 'true');

      drag(dialog, 'dragenter', [gcode('benchy.gcode')]);
      flushSync();
      expect(sheet()).toBeNull();
      drag(dialog, 'drop', [gcode('benchy.gcode')]);

      expect(onFiles).not.toHaveBeenCalled();
      dialog.remove();
    });

    it('ignores a drag that carries no files', async () => {
      await render(FileDropzone, { props: { onFiles: vi.fn(), page: true } });
      const spot = elsewhere();
      const text = new DataTransfer();
      text.setData('text/plain', 'hello');
      const over = new DragEvent('dragover', {
        bubbles: true,
        cancelable: true,
        dataTransfer: text,
      });
      spot.dispatchEvent(
        new DragEvent('dragenter', { bubbles: true, dataTransfer: text }),
      );
      spot.dispatchEvent(over);
      flushSync();

      expect(over.defaultPrevented).toBe(false);
      expect(sheet()).toBeNull();
      spot.remove();
    });

    it('hands a page drop only to the zone mounted last', async () => {
      const first = vi.fn();
      const second = vi.fn();
      await render(FileDropzone, { props: { onFiles: first, page: true } });
      const later = await render(FileDropzone, {
        props: { onFiles: second, page: true },
      });
      const spot = elsewhere();

      drag(spot, 'drop', [gcode('benchy.gcode')]);
      expect(first).not.toHaveBeenCalled();
      expect(second).toHaveBeenCalledTimes(1);

      // Once the later zone goes, the earlier one owns the page again.
      await later.unmount();
      drag(spot, 'drop', [gcode('benchy.gcode')]);
      expect(first).toHaveBeenCalledTimes(1);
      spot.remove();
    });

    it('takes back every window listener it added when it unmounts', async () => {
      const added = vi.spyOn(window, 'addEventListener');
      const removed = vi.spyOn(window, 'removeEventListener');
      const drags = (spy: typeof added): number =>
        spy.mock.calls.filter(
          ([type]) => type.startsWith('drag') || type === 'drop',
        ).length;

      for (let round = 0; round < 3; round += 1) {
        const mounted = await render(FileDropzone, {
          props: { onFiles: vi.fn(), page: true },
        });
        await mounted.unmount();
      }

      expect(drags(added)).toBe(12);
      expect(drags(removed)).toBe(drags(added));
      added.mockRestore();
      removed.mockRestore();
    });
  });
});
