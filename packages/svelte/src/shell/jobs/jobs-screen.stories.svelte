<script module lang="ts">
  import { defineMeta } from '@storybook/addon-svelte-csf';

  import {
    jobsRead,
    nothingPrinted,
    readRefused,
    remembering,
    stillReading,
  } from './fixtures';
  import { START_PRINT_VERB } from '../../data/services/print';
  import {
    type PrintOutcome,
    type PrintRequest,
    STATE_PATH,
  } from '../../domain';
  import { fakeDevice } from '../../test/fakes';
  import JobsScreen from './jobs-screen.svelte';

  const { Story } = defineMeta({
    title: 'Parts/Screen components/Jobs',
    component: JobsScreen,
  });

  // The smallest page size on purpose.
  const paged = remembering({ pageSize: 5 });

  const onPrint = (request: PrintRequest): Promise<PrintOutcome> =>
    Promise.resolve({
      commandId: 'cmd-1',
      faultCode: undefined,
      machineReason: undefined,
      ok: true,
      path: request.path,
      transferId: undefined,
    });

  // Every printer that ran a print, waiting with an empty bed.
  const devices = [
    'prn-rack-3',
    'prn-core',
    'prn-xl',
    'prn-mk3s',
    'prn-mini',
  ].map((id) => ({
    ...fakeDevice(id),
    capabilities: [START_PRINT_VERB],
    state: { [STATE_PATH]: 'IDLE' },
  }));
</script>

<!-- The React specification's own states. -->

<Story name="Listing" asChild>
  <JobsScreen jobs={jobsRead()} store={paged} />
</Story>

<Story name="Loading" asChild>
  <JobsScreen jobs={stillReading()} store={paged} />
</Story>

<Story name="Nothing Printed Yet" asChild>
  <JobsScreen jobs={nothingPrinted()} store={paged} />
</Story>

<Story name="The Read Was Refused" asChild>
  <JobsScreen jobs={readRefused()} store={paged} />
</Story>

<Story name="The Rows View" asChild>
  <JobsScreen jobs={jobsRead()} store={remembering({ view: 'rows' })} />
</Story>

<Story name="Some Fields Put Away" asChild>
  <JobsScreen
    jobs={jobsRead()}
    store={remembering({ fields: ['file', 'state', 'started'], pageSize: 5 })}
  />
</Story>

<!-- A host that can send a file back to the printer that ran it. -->
<Story name="Print Again Is Offered" asChild>
  <JobsScreen jobs={jobsRead()} store={paged} {devices} {onPrint} />
</Story>

<!-- The table is offered wherever it fits. -->
<Story name="In A Narrow Panel" asChild>
  <div class="max-w-md">
    <JobsScreen jobs={jobsRead()} store={paged} />
  </div>
</Story>

<!-- What a module draws in the job-column slot: one value a print, under the
     heading its manifest names. -->
{#snippet cell(columnId: string, jobId: string)}
  <span class="tabular-nums" data-column={columnId} data-job={jobId}>38 g</span>
{/snippet}

<Story name="With A Module Column" asChild>
  <JobsScreen
    jobs={jobsRead()}
    store={paged}
    columns={[{ id: 'com.example.spools.used', label: 'Filament' }]}
    {cell}
  />
</Story>
