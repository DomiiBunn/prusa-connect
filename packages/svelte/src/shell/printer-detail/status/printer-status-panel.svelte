<script lang="ts">
  import FileCode2Icon from '@lucide/svelte/icons/file-code-2';
  import WifiOffIcon from '@lucide/svelte/icons/wifi-off';

  import {
    PRINTER_STATE_PROGRESS_TONE,
    PRINTER_STATE_SEVERITY_TIER,
  } from '../../../domain';
  import {
    formatDate,
    formatDateTime,
    formatDuration,
    formatUnit,
    t,
  } from '../../../i18n';
  import {
    AspectRatio,
    Badge,
    Card,
    CardContent,
    ClippedName,
    EmptyState,
    FileThumbnail,
    Progress,
    Skeleton,
  } from '../../../primitives';
  import CameraPicture from '../../printer-cameras/camera-picture.svelte';
  import FileAccess from './file-access.svelte';
  import {
    jobOf,
    PRINTER_STATE_VARIANT,
    SPLIT_LEADING,
    SPLIT_ROW,
    SPLIT_TRAILING,
    stateOf,
    wireStateOf,
  } from './presentation';
  import PrintControls from './print-controls.svelte';
  import PrinterDialog from './printer-dialog.svelte';
  import type { PrinterStatusPanelProps } from './types';

  let {
    device,
    telemetry,
    commands,
    now,
    camera,
    loading = false,
    transfers,
    uploads,
    onCancelUpload,
    history,
    recent,
    files,
    historyHref,
    thumbnailFor,
    onUpload,
    uploadBlock,
    keep,
    checkingStorages,
    onRecheck,
  }: PrinterStatusPanelProps = $props();

  /** The one label treatment every block on this panel names its parts with. */
  const BLOCK_LABEL = 'text-xs font-medium text-muted-foreground';

  const MS_PER_SECOND = 1000;

  const printerState = $derived(stateOf(telemetry, device));
  const variant = $derived(PRINTER_STATE_VARIANT[printerState]);
  const job = $derived(jobOf(telemetry, device));

  // A bar that advances on a stopped print would be a lie.
  const tone = $derived(
    variant === 'notReporting'
      ? 'muted'
      : PRINTER_STATE_PROGRESS_TONE[printerState],
  );

  const split = $derived(variant === 'running' || variant === 'trouble');
  const showsCamera = $derived(split && camera !== undefined);

  // The camera whose live view runs, so another camera starts on its still.
  let playingName = $state<string | undefined>(undefined);
  const playing = $derived(
    camera !== undefined && playingName === camera.camera.name,
  );

  // Counted from when the time left arrived. A default taken when the panel
  // opened would count it from then, however long ago that was.
  const finishesAt = $derived(
    job.remainingSeconds === undefined || variant !== 'running'
      ? undefined
      : formatDate(
          new Date(
            (now?.getTime() ?? Date.now()) +
              job.remainingSeconds * MS_PER_SECOND,
          ),
          {
            timeStyle: 'short',
          },
        ),
  );
  const remaining = $derived(
    job.remainingSeconds === undefined || variant !== 'running'
      ? ''
      : formatDuration(job.remainingSeconds),
  );
  const elapsed = $derived(
    job.elapsedSeconds === undefined || variant !== 'running'
      ? ''
      : formatDuration(job.elapsedSeconds),
  );

  // What is left and what is behind.
  const timing = $derived(
    [
      remaining === ''
        ? undefined
        : t('printerDetailStatus.remaining', { duration: remaining }),
      elapsed === ''
        ? undefined
        : t('printerDetailStatus.elapsed', { duration: elapsed }),
    ]
      .filter((part) => part !== undefined)
      .join(' · '),
  );

  // The bar is named after the print it measures.
  const progressLabel = $derived(
    job.name === undefined
      ? t('printerDetailStatus.progress.label')
      : t('printerDetailStatus.progress.labelFor', { name: job.name }),
  );

  // What the host has found wrong with this printer.
  const note = $derived(device.issues?.[0]?.detail);
  const noteClass = $derived(
    PRINTER_STATE_SEVERITY_TIER[printerState] === 'critical'
      ? 'text-destructive-text'
      : 'text-warning-text',
  );

  const departed = $derived(device.departed?.at);
  const hasReported = $derived(Object.keys(device.state).length > 0);
  const notReportingNote = $derived.by(() => {
    if (departed !== undefined) {
      return t('printerDetailStatus.notReporting.lastSeen', {
        when: formatDateTime(departed),
      });
    }

    return t(
      hasReported
        ? 'printerDetailStatus.notReporting.unreachable'
        : 'printerDetailStatus.notReporting.never',
    );
  });

  // A bar with no reading draws itself as indeterminate — a full, pulsing
  // track. On a printer holding no print at all that reads as a print running
  // at full tilt, so the bar belongs only to a print that exists.
  const measured = $derived(job.name !== undefined || job.progress !== null);

  // A stopped send leaves the machine naming a file it never kept, so the
  // files the prints name are looked up before Print again is offered, and
  // the printer reads the folder again for a print that was stopped or failed.
  $effect(() => {
    const ended = [history?.jobs?.last, ...(recent ?? [])];
    void files?.checkHeld(
      [job.path, ...ended.map((row) => row?.path)].filter(
        (path) => path !== undefined,
      ),
      [
        printerState === 'STOPPED' || printerState === 'ERROR'
          ? job.path
          : undefined,
        ...ended
          .filter(
            (row) => row?.outcome === 'cancelled' || row?.outcome === 'failed',
          )
          .map((row) => row?.path),
      ].filter((path) => path !== undefined),
    );
  });
</script>

{#snippet progressBar()}
  <div class="flex items-center gap-3">
    <Progress
      value={job.progress}
      {tone}
      aria-label={progressLabel}
      class="h-1 flex-1"
    />
    {#if job.progress !== null}
      <span
        class="shrink-0 font-mono text-xs text-muted-foreground tabular-nums"
      >
        {formatUnit(job.progress, '%')}
      </span>
    {/if}
  </div>
{/snippet}

{#snippet jobName()}
  {#if job.name === undefined}
    <span class="min-w-0 flex-1 truncate text-base">{t('printers.noJob')}</span>
  {:else}
    <!-- The name asks for 14rem: the plate and the clipped name's tail, with
         room for part of its head. In a row narrower than that plus the
         controls, the controls move to a line of their own, so they never
         draw over the name. -->
    <div class="flex min-w-0 flex-1 basis-56 items-center gap-3">
      {@render plate(job.name)}
      <ClippedName name={job.name} class="min-w-0 flex-1 font-mono text-base" />
    </div>
  {/if}
{/snippet}

{#snippet plate(name: string)}
  <FileThumbnail
    {name}
    glyph={FileCode2Icon}
    class="shrink-0"
    thumbnailUrl={job.path === undefined ? undefined : thumbnailFor?.(job.path)}
  />
{/snippet}

{#snippet controls()}
  <PrintControls
    {device}
    {commands}
    state={wireStateOf(telemetry, device)}
    path={job.path}
    held={job.path === undefined ? undefined : files?.holds(job.path)}
  />
{/snippet}

{#snippet currentPrint()}
  <div
    class={[
      'flex min-w-0 flex-1 flex-col gap-2',
      showsCamera && SPLIT_TRAILING,
    ]}
  >
    <span class={BLOCK_LABEL}>{t('printerDetailStatus.currentPrint')}</span>
    <div class="flex flex-col gap-3">
      <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-2">
        {@render jobName()}
        {@render controls()}
      </div>
      {@render progressBar()}
      {#if finishesAt !== undefined || timing !== ''}
        <div class="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          {#if finishesAt !== undefined}
            <span class="font-mono text-2xl whitespace-nowrap tabular-nums">
              {t('printerDetailStatus.finishes', { when: finishesAt })}
            </span>
          {/if}
          {#if timing !== ''}
            <span class="font-mono text-sm text-muted-foreground tabular-nums">
              {timing}
            </span>
          {/if}
        </div>
      {/if}
      {#if variant === 'trouble' && note !== undefined}
        <p class={['text-sm', noteClass]}>{note}</p>
      {/if}
    </div>
  </div>
{/snippet}

<div class="@container/now flex flex-col gap-6">
  {#if loading}
    <!-- The panel's own shape while the record is on its way. -->
    <Card>
      <CardContent class="flex flex-col gap-6">
        <div
          class={SPLIT_ROW}
          aria-busy="true"
          aria-label={t('printerDetailStatus.loading')}
        >
          <div class={['flex flex-col gap-2', SPLIT_LEADING]}>
            <AspectRatio ratio={16 / 9}>
              <Skeleton class="size-full" />
            </AspectRatio>
          </div>
          <div class={['flex min-w-0 flex-1 flex-col gap-3', SPLIT_TRAILING]}>
            <Skeleton class="h-5 w-2/3" />
            <Skeleton class="h-1 w-full" />
            <Skeleton class="h-8 w-40" />
          </div>
        </div>
      </CardContent>
    </Card>
  {:else}
    <PrinterDialog {device} {telemetry} {commands} />

    <Card>
      <CardContent class="flex flex-col gap-6">
        {#if variant === 'notReporting'}
          <EmptyState
            icon={WifiOffIcon}
            heading={t('printerDetailStatus.notReporting.heading')}
            headingLevel="h2"
            description={notReportingNote}
          />
          {#if job.name !== undefined}
            <div class="flex min-w-0 flex-col gap-3">
              <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
                {@render plate(job.name)}
                <ClippedName
                  name={job.name}
                  class="min-w-0 font-mono text-sm"
                />
                <Badge variant="outline">
                  {t('printerDetailStatus.lastJob.badge')}
                </Badge>
              </div>
              {@render progressBar()}
            </div>
          {/if}
        {:else if split}
          <div class={['min-w-0', SPLIT_ROW]}>
            {#if showsCamera && camera !== undefined}
              <div class={['flex flex-col gap-2', SPLIT_LEADING]}>
                <span class={BLOCK_LABEL}>
                  {t('printerDetailStatus.camera.label')}
                </span>
                <!-- Keyed, so another camera starts as a new picture: playing
                     while it is seen, and never held by a stop the last one
                     was given. -->
                {#key camera.camera.name}
                  <CameraPicture
                    {...camera}
                    alt={t('printerDetailStatus.camera.alt')}
                    autoplay
                    bind:playing={
                      () => playing,
                      (on: boolean) => {
                        playingName = on ? camera.camera.name : undefined;
                      }
                    }
                    class="rounded-sm"
                  >
                    {#if playing}
                      <Badge
                        variant="secondary"
                        class="absolute start-2 top-2 gap-1.5 uppercase"
                      >
                        <span
                          class="size-1.5 rounded-full bg-destructive"
                          aria-hidden="true"
                        ></span>
                        {t('printerDetailStatus.camera.live')}
                      </Badge>
                    {/if}
                  </CameraPicture>
                {/key}
              </div>
            {/if}
            {@render currentPrint()}
          </div>
        {:else}
          <div class="flex flex-wrap items-center gap-x-3 gap-y-4">
            <div class="flex min-w-0 flex-1 flex-col gap-2">
              {@render jobName()}
              {#if measured}
                {@render progressBar()}
              {/if}
            </div>
            <div class="ms-auto flex flex-wrap items-center gap-2">
              {@render controls()}
            </div>
          </div>
        {/if}
      </CardContent>
    </Card>

    <FileAccess
      {device}
      {commands}
      state={wireStateOf(telemetry, device)}
      stale={variant === 'notReporting'}
      {transfers}
      {uploads}
      {onCancelUpload}
      {history}
      {recent}
      {files}
      {historyHref}
      {thumbnailFor}
      {onUpload}
      {uploadBlock}
      {keep}
      {checkingStorages}
      {onRecheck}
    />
  {/if}
</div>
