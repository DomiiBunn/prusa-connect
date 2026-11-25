// The module's setup registers its card on the Printer tab and its How to
// tab on the module's page under Settings, Extensions.
import type { ModuleHost } from '@prusa/connect-ui/slots';
import { afterEach, describe, expect, it } from 'vitest';

import setup from './index';
import { t } from './test/fakes';

const controllers: AbortController[] = [];

afterEach(() => {
  for (const controller of controllers.splice(0)) controller.abort();
});

describe('the module setup', () => {
  it('registers the Printer tab card and the How to tab', async () => {
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
        deviceInfoSection: record('deviceInfoSection'),
        settings: record('settings'),
      },
    } as unknown as ModuleHost);

    expect(calls).toEqual(['deviceInfoSection:prusalink', 'settings:how-to']);
  });
});
