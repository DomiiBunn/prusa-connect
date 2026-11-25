import type { ResourceInstance } from '@prusa/connect-ui/data';

// Every contribution an operator may hide, keyed as the server's
// hidden_contributions entries are: slot key, then the path of a page or the
// id of anything else. A module's settings tabs and Printer tab cards are
// never hidden, and a page kept out of the sidebar has no entry to hide, so
// none of them is listed.
const SLOT_KINDS = [
  ['pages', 'page'],
  ['device_card', 'deviceCard'],
  ['device_section', 'deviceSection'],
  ['device_tab', 'deviceTab'],
  ['device_action', 'deviceAction'],
  ['file_action', 'fileAction'],
  ['print_section', 'printSection'],
  ['job_column', 'jobColumn'],
  ['job_section', 'jobSection'],
] as const;

export interface Contribution {
  readonly key: string;
  readonly title: string;
  readonly kind: (typeof SLOT_KINDS)[number][1];
}

export const contributionsOf = (
  instance: ResourceInstance | undefined,
): Contribution[] =>
  SLOT_KINDS.flatMap(([slot, kind]) =>
    (instance?.ui?.contributes[slot] ?? [])
      .filter((one) => !('hidden' in one && one.hidden))
      .map((one) => ({
        key: `${slot}/${'path' in one ? one.path : one.id}`,
        title: one.title,
        kind,
      })),
  );
