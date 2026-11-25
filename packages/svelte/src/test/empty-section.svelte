<!-- A registered job section that draws nothing, as a module draws for a
     print it knows nothing about. -->
<script lang="ts">
  let { jobId }: { jobId: string } = $props();
</script>

{#if jobId === ''}{jobId}{/if}
