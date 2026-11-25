<!-- This product's page for one print. -->
<script lang="ts">
  import {
    deviceThumbnailUrlOf,
    isTemporarySource,
    lastSentBefore,
  } from '@prusa/connect-ui/data';
  import { JobDetailScreen, printerHref } from '@prusa/connect-ui/shell';
  import { SlotOutlet } from '@prusa/connect-ui/slots';
  import { onMount } from 'svelte';

  import { client } from '../client';
  import { PATHS, printedHref, storageFileHref } from '../routes';
  import type { AppContext } from '../screens';

  let { ctx }: { ctx: AppContext } = $props();

  const jobId = $derived(ctx.params.id ?? '');

  const source = $derived(ctx.jobDetail(jobId));

  // The print names the printer it ran on once it has been read.
  const deviceId = $derived(source.job?.device_id ?? '');

  // Only a transfer says which file on this server the printer printed. The
  // printer is known once the print has been read, and nothing is read before.
  const transfers = $derived(
    deviceId === '' ? undefined : ctx.transfers(deviceId),
  );

  $effect(() => {
    void transfers?.load();
  });

  // The send that put this print's file on the printer.
  // A file sent from a temporary copy is no longer on this server, so it
  // is read as a print this server did not send.
  const sentFrom = $derived.by(() => {
    if (source.job === undefined || transfers === undefined) return undefined;
    const sent = lastSentBefore(
      transfers.rows,
      source.job.path,
      source.job.started,
    );

    return sent === undefined || isTemporarySource(sent.source)
      ? undefined
      : sent;
  });

  // A print this server did not send is read out of the file on the printer.
  const thumbnailUrl = $derived.by(() => {
    if (sentFrom !== undefined) {
      return ctx.storage.thumbnailUrl(sentFrom.storage, sentFrom.source);
    }
    if (source.job === undefined) return undefined;

    return deviceThumbnailUrlOf(client, deviceId, source.job.path);
  });

  const device = $derived(ctx.fleet.devices.find((row) => row.id === deviceId));

  $effect(() => {
    void source.load();
  });

  // The printer the print ran on is drawn beside it.
  $effect(() => {
    void ctx.fleet.load();
    void ctx.deviceStates.read();
  });

  // The names of the models a driver declared, for the printer's icon.
  onMount(() => {
    void ctx.deviceModels.read();
  });

  // The dialog needs the moves of one kind, the printer's own.
  const transitions = $derived(
    device === undefined ? [] : ctx.deviceStates.transitionsOf([device.kind]),
  );
</script>

<!-- A module's sections about this print, each a card under the details. -->
{#snippet sections(id: string)}
  <SlotOutlet screen="job-detail" name="job-section" jobId={id} />
{/snippet}

<JobDetailScreen
  {sections}
  {jobId}
  {source}
  {device}
  jobsHref={PATHS.jobs}
  printerHref={device === undefined ? PATHS.printers : printerHref(device.id)}
  fileHref={() =>
    sentFrom === undefined
      ? undefined
      : storageFileHref(sentFrom.storage, sentFrom.source)}
  {thumbnailUrl}
  {transitions}
  onPrint={ctx.print}
  onPrinted={(printers: readonly string[]) => {
    ctx.go(printedHref(printers));
  }}
/>
