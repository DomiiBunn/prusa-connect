<script lang="ts">
  import type { ClassValue } from 'svelte/elements';

  import type { UploadBlock } from '../../../domain';
  import { t } from '../../../i18n';
  import { FileDropzone } from '../../../primitives';
  import { SaveToStorage, type SaveToStorageProps } from '../save-to-storage';
  import UploadBlocked from '../upload-blocked.svelte';
  import type { SendsIndicatorRow } from '../../sends-indicator';
  import PrinterFileTransfers from './printer-file-transfers.svelte';
  import type {
    PrinterFileTransfer,
    PrinterFileTransfers as Port,
  } from './types';

  /**
   * What the printer takes, as the file input spells it: a print, or a
   * firmware image to flash. The firmware refuses a project file, so a .3mf
   * would be saved here and never reach the drive.
   */
  const UPLOAD_ACCEPT = '.gcode,.bgcode,.bbf';

  let {
    class: className,
    dropzone,
    driveName,
    queue,
    transfers,
    uploads,
    onCancelUpload,
    onFiles,
    block,
    checking = false,
    onRecheck,
    keep,
  }: {
    class?: ClassValue | undefined;
    /** Undefined where this drive takes nothing, which hides the zone. */
    dropzone?: unknown;
    driveName: string;
    queue: readonly PrinterFileTransfer[];
    transfers?: Port | undefined;
    uploads?: readonly SendsIndicatorRow[] | undefined;
    onCancelUpload?: ((id: string) => void) | undefined;
    onFiles: (dropped: File[]) => void;
    /** Why this drive takes nothing, said where the zone would be. */
    block?: UploadBlock | undefined;
    checking?: boolean | undefined;
    onRecheck?: (() => void) | undefined;
    /** Whether a dropped file is kept on this server, under the zone. */
    keep?: SaveToStorageProps | undefined;
  } = $props();
</script>

<div class={['flex flex-col gap-6', className]}>
  {#if dropzone}
    <div class="flex flex-col gap-3">
      <h2 class="text-sm font-medium">
        {t('printerDetailFiles.upload.heading', { name: driveName })}
      </h2>
      <!-- Its natural height rather than the listing's. -->
      <FileDropzone accept={UPLOAD_ACCEPT} page {onFiles} />
      {#if keep}
        <SaveToStorage {...keep} />
      {/if}
    </div>
  {:else if block}
    <UploadBlocked {block} {checking} {onRecheck} />
  {/if}

  <PrinterFileTransfers {queue} {transfers} {uploads} {onCancelUpload} />
</div>
