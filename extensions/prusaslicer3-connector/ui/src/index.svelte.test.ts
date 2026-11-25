// The module's setup registers "Set as current" only inside PrusaSlicer.
import type { ModuleHost } from '@prusa/connect-ui/slots';
import { afterEach, describe, expect, it } from 'vitest';

import setup from './index';
import { t } from './test/fakes';

type Window = typeof globalThis & { _prusaSlicer?: unknown };

const page = globalThis as Window;
const controllers: AbortController[] = [];

afterEach(() => {
  delete page._prusaSlicer;
  for (const controller of controllers.splice(0)) controller.abort();
});

const registered = async (): Promise<string[]> => {
  const calls: string[] = [];
  const controller = new AbortController();
  controllers.push(controller);
  const record =
    (kind: string) =>
    (id: string): void => {
      calls.push(`${kind}:${id}`);
    };
  await setup({
    t,
    signal: controller.signal,
    client: {},
    register: {
      page: record('page'),
      settings: record('settings'),
      deviceHeader: record('deviceHeader'),
      deviceRow: record('deviceRow'),
    },
  } as unknown as ModuleHost);

  return calls;
};

describe('the module setup', () => {
  it('registers the page and the How to tab and no printer action in a browser', async () => {
    expect(await registered()).toEqual([
      'page:select-printer',
      'settings:how-to',
    ]);
  });

  it('registers Set as current on the listing and the page inside PrusaSlicer', async () => {
    page._prusaSlicer = { postMessage: () => undefined };

    expect(await registered()).toEqual([
      'page:select-printer',
      'settings:how-to',
      'deviceHeader:set-as-current',
      'deviceRow:set-as-current',
    ]);
  });
});
