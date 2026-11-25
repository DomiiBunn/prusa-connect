import { describe, expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';

import { holdHost } from '../../host';
import { stubHost } from '../../test/fakes';
import HowTo from './how-to.svelte';

describe('the How to tab of the module', () => {
  it('says the connector is set up on each printer and where', async () => {
    holdHost(stubHost().host);
    await render(HowTo);

    await expect
      .element(page.getByText(/set up for each printer, not for the server/))
      .toBeInTheDocument();
    await expect
      .element(page.getByText(/under the Connection card/))
      .toBeInTheDocument();
  });
});
