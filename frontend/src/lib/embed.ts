// Whether the page is shown embedded: a module page or the app pairing page
// opened with `?embed=1` draws without the sidebar and the header
// (ADR-20261112-2eb rule 6). PrusaSlicer opens such a page in its own window.
// On the app pairing page the person must not leave the approval through the
// navigation. The page still needs the person signed in, as every page does.
import { PATHS } from './routes';

/** The query parameter that asks for the embedded page. */
export const EMBED_PARAM = 'embed';

/**
 * True for a module page or the app pairing page whose address carries
 * `embed=1`.
 */
export const isEmbedded = (
  pattern: string | undefined,
  search: string,
): boolean =>
  (pattern === PATHS.modulePage || pattern === PATHS.pair) &&
  new URLSearchParams(search).get(EMBED_PARAM) === '1';
