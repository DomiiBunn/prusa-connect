<!--
  The How to tab on the module's page under Settings, Extensions.
-->
<script module lang="ts">
  import { defineMeta } from '@storybook/addon-svelte-csf';

  import { holdHost } from '../../host';
  import { stubHost } from '../../test/fakes';
  import HowTo from './how-to.svelte';

  holdHost(stubHost().host);

  const { Story } = defineMeta({
    title: 'PrusaSlicer 2 Connector/HowTo',
    component: HowTo,
  });
</script>

<Story name="How to" />
<Story name="Phone" globals={{ viewport: { value: 'phone' } }} />
