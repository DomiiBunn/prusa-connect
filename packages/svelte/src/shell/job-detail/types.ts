import type { Snippet } from 'svelte';

import type { JobDetail } from '../../data';
import type { PrintJob } from '../../data/services/jobs';
import type { Device, PrintAction, StateTransition } from '../../domain';

/** One print, as the core records it. */
export type JobRow = PrintJob;

/** What the screen reads of one print. */
export type JobSource = Pick<JobDetail, 'error' | 'job' | 'loaded'>;

export interface JobDetailScreenProps {
  jobId: string;
  source: JobSource;
  /** The printer it ran on, once the fleet holds it. */
  device?: Device | undefined;
  /** Where the trail goes back to. */
  jobsHref: string;
  printerHref: string;
  /**
   * Where the file this print ran is drawn, from its path on the printer.
   * Undefined where the host knows no page for that file.
   */
  fileHref?: ((path: string) => string | undefined) | undefined;
  /**
   * The plate this print was sliced from. Absent unless this server still
   * holds the file it sent the printer.
   */
  thumbnailUrl?: string | undefined;
  /** The moves between states the device kinds declared, for the print dialog. */
  transitions?: readonly StateTransition[] | undefined;
  /** The printers held by another operator or by a command still running. */
  locked?: readonly string[] | undefined;
  /** Starts a print. Without it the page offers no Print again. */
  onPrint?: PrintAction | undefined;
  /** Called with the printers' ids once every one of them has taken the file. */
  onPrinted?: ((deviceIds: readonly string[]) => void) | undefined;
  /**
   * The job-section slot's outlet, drawn under the details. The outlet draws a
   * card for each section a module registered, and nothing when there is none.
   */
  sections?: Snippet<[jobId: string]> | undefined;
}
