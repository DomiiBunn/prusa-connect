import type { ModuleHost } from '@prusa/connect-ui/slots';
import { describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';

import { holdSession } from '../../session';
import { farm, t } from '../../test/fakes';
import HowTo from './how-to.svelte';

describe('the How to tab of the module', () => {
  it('says the connector is set up once in PrusaSlicer, with the address under step 1', async () => {
    holdSession({
      host: {
        t,
        signal: new AbortController().signal,
      } as unknown as ModuleHost,
      api: {
        listSlicerPrinters: () => Promise.resolve({ printers: farm }),
      },
    });
    await render(HowTo, { origin: 'http://192.168.1.20:8080' });

    await expect
      .element(page.getByText(/set up once, in PrusaSlicer/))
      .toBeInTheDocument();
    const address = page.getByLabelText('Server address');
    await expect.element(address).toHaveValue('http://192.168.1.20:8080');
    expect(address.element().closest('li')?.textContent).toContain(
      'paste this server',
    );
  });
});
