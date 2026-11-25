<script module lang="ts">
  import { defineMeta } from '@storybook/addon-svelte-csf';

  import FileDropzone from './file-dropzone.svelte';

  const { Story } = defineMeta({
    title: 'Primitives/FileDropzone',
    component: FileDropzone,
    args: { onFiles: () => undefined, accept: '.gcode,.bgcode' },
  });
</script>

<Story name="Default" />

<Story name="Disabled" args={{ disabled: true }} />

<!-- One row, for a screen whose subject is something else. The line naming the
     formats goes; the `sr-only` label still names them, so a screen reader
     loses nothing. -->
<Story name="Compact" args={{ compact: true }} />

<!-- Takes a file dropped anywhere on the window. The sheet over the window
     shows only while files are dragged over it, so at rest this looks like
     Default. -->
<Story name="Page" args={{ page: true }} />

<Story name="Dragging Over Highlights The Zone" />

<Story
  name="Rejected Type Is Surfaced And Filtered Even When Accepted"
  tags={['!dev', '!autodocs']}
/>

<Story name="Multi File Pick Accepts A Batch" tags={['!dev', '!autodocs']} />

<Story
  name="Keyboard Activation Reaches The File Input"
  tags={['!dev', '!autodocs']}
/>
