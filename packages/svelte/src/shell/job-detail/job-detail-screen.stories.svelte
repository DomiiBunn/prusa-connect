<script module lang="ts">
  import { defineMeta } from '@storybook/addon-svelte-csf';

  import { START_PRINT_VERB } from '../../data/services/print';
  import {
    type PrintOutcome,
    type PrintRequest,
    STATE_PATH,
  } from '../../domain';
  import { fakeDevice } from '../../test/fakes';
  import JobDetailScreen from './job-detail-screen.svelte';
  import type { JobRow, JobSource } from './types';

  const { Story } = defineMeta({
    title: 'Parts/Screen components/Job Detail',
    component: JobDetailScreen,
  });

  const JOB_ID = '0199e3a4-5bb5-7aac-8a2c-9c3982e6373f';

  const printer = {
    ...fakeDevice('p0', 'MK4S #1042'),
    capabilities: [START_PRINT_VERB],
    model: 'MK4S',
    state: { [STATE_PATH]: 'IDLE' },
  };

  // Fixed, never `new Date()`, so nothing here drifts between runs.
  const finished: JobRow = {
    display_name: 'Bracket_Left_Reinforced_v4_FINAL.gcode',
    ended: '2026-08-19T09:10:00Z',
    id: JOB_ID,
    device_job_id: '412',
    device_id: 'p0',
    path: '/usb/brackets/Bracket_Left_Reinforced_v4_FINAL.bgcode',
    progress: 100,
    started: '2026-08-19T07:02:00Z',
    state: 'FINISHED',
    time_remaining_ms: 0,
  };

  const printing: JobRow = {
    display_name: 'Camera_Mount_Bracket_v3.gcode',
    id: JOB_ID,
    device_job_id: '412',
    device_id: 'p0',
    path: '/usb/mounts/Camera_Mount_Bracket_v3.bgcode',
    progress: 42,
    started: '2026-08-19T09:12:00Z',
    state: 'PRINTING',
    time_remaining_ms: 2_280_000,
  };

  const failed: JobRow = {
    ...finished,
    display_name: 'enclosure_frame_final_v6_verified_assembly_left.gcode',
    ended: '2026-08-19T06:46:00Z',
    path: '/usb/enclosure/enclosure_frame_final_v6_verified_assembly_left.bgcode',
    progress: 3,
    started: '2026-08-19T06:40:00Z',
    state: 'ERROR',
  };

  const stopped: JobRow = {
    ...finished,
    display_name: 'calibration_cube_pla.gcode',
    ended: '2026-08-17T09:30:00Z',
    path: '/usb/calibration/calibration_cube_pla.bgcode',
    progress: 18,
    started: '2026-08-17T09:18:00Z',
    state: 'STOPPED',
  };

  const answered = (job?: JobRow): JobSource => ({
    error: undefined,
    job,
    loaded: true,
  });

  const onPrint = (request: PrintRequest): Promise<PrintOutcome> =>
    Promise.resolve({
      commandId: 'cmd-1',
      faultCode: undefined,
      machineReason: undefined,
      ok: true,
      path: request.path,
      transferId: undefined,
    });

  const fileHref = (path: string) =>
    `/storage/local/file/${encodeURIComponent(path)}`;
</script>

<!-- The ordinary case: it printed, it finished, nothing to decide. -->
<Story name="Finished" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(finished)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
  />
</Story>

<Story name="Printing" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(printing)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
  />
</Story>

<!-- A failure is an alert above the record and not a field inside it. -->
<Story name="Failed" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(failed)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
  />
</Story>

<Story name="Stopped" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(stopped)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
  />
</Story>

<!-- The caller cannot send this file again, so nothing offers to. -->
<Story name="Nothing To Offer" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(finished)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
  />
</Story>

<!-- The fleet does not hold the printer this print ran on. -->
<Story name="Printer Not In The Fleet" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(finished)}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
  />
</Story>

<!-- The path names the print before the server answers. -->
<Story name="Loading" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={{
      error: undefined,
      job: undefined,
      loaded: false,
    }}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
  />
</Story>

<!-- The server answered and holds no such print. -->
<Story name="No Such Print" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered()}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
  />
</Story>

<Story name="The Server Refused" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={{
      error: {
        code: 'core.device/NOT_FOUND',
        traceId: undefined,
        message: 'no device with id p0 on this server',
        params: {},
        status: 404,
      },
      job: undefined,
      loaded: false,
    }}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
  />
</Story>

<Story name="On A Phone" asChild>
  <div class="w-full max-w-xs">
    <JobDetailScreen
      jobId={JOB_ID}
      source={answered(failed)}
      device={printer}
      jobsHref="/jobs"
      printerHref="/printers/p0"
      {fileHref}
      {onPrint}
    />
  </div>
</Story>

<!-- What a module draws in the job-section slot, under the print's details. -->
{#snippet sections(jobId: string)}
  <section class="flex flex-col gap-2" data-job={jobId}>
    <h3 class="text-sm font-semibold">Filament</h3>
    <p class="text-sm text-muted-foreground">Tool 1 · Prusament PLA · 38 g</p>
  </section>
{/snippet}

<Story name="With A Module Section" asChild>
  <JobDetailScreen
    jobId={JOB_ID}
    source={answered(finished)}
    device={printer}
    jobsHref="/jobs"
    printerHref="/printers/p0"
    {fileHref}
    {onPrint}
    {sections}
  />
</Story>
