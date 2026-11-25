import type { ClassValue, HTMLAttributes } from 'svelte/elements';

export interface FileDropzoneProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'class' | 'children' | 'ondrop' | 'ondragover' | 'ondragleave'
> {
  /** Every file a drop or a pick accepted. It is never called with an empty list. */
  onFiles: (files: File[]) => void;
  /**
   * What the zone takes, as the file input writes it: `.gcode,.bgcode`,
   * `text/plain` or `image/*`, separated by commas. Leave it out to take
   * everything.
   */
  accept?: string | undefined;
  /**
   * Refuses a drop and a pick, and draws the zone in the muted border. The
   * caller sets it while an upload is already running, so a second batch
   * cannot start on top of the first.
   */
  disabled?: boolean;
  /**
   * One row instead of a panel, for a screen whose subject is something else.
   * It drops the line naming the accepted formats, which the `sr-only` label
   * still carries.
   */
  compact?: boolean;
  /**
   * Takes a file dropped anywhere on the window, not only on the zone, and
   * covers the window with a sheet while files are dragged over it. For a
   * screen whose main job is taking files. Of several page zones mounted at
   * once, the one mounted last takes the drop.
   */
  page?: boolean;
  /**
   * What the zone takes, in the caller's words: "Signed update bundle". It
   * names the file input in place of the generic "Upload files", formats
   * included, so a caller that sets `accept` on a compact zone says them here.
   */
  label?: string | undefined;
  class?: ClassValue | undefined;
}
