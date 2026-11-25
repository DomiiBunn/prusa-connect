<!--
  The How to tab on the module's page under Settings, Extensions.
-->
<script module lang="ts">
  import { defineMeta } from '@storybook/addon-svelte-csf';
  import type { ModuleHost } from '@prusa/connect-ui/slots';

  import { holdSession } from '../../session';
  import { farm, t } from '../../test/fakes';
  import HowTo from './how-to.svelte';

  holdSession({
    host: { t, signal: new AbortController().signal } as unknown as ModuleHost,
    api: {
      listSlicerPrinters: () => Promise.resolve({ printers: farm }),
    },
  });

  const { Story } = defineMeta({
    title: 'PrusaSlicer 3/HowTo',
    component: HowTo,
    args: { origin: 'http://192.168.1.20:8080' },
  });
</script>

<Story name="How to" />
<Story name="Phone" globals={{ viewport: { value: 'phone' } }} />
