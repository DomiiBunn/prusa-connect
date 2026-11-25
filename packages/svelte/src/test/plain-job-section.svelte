<!-- A job section that works, for the slot tests. -->
<script lang="ts">
  let { jobId }: { jobId: string } = $props();
</script>

<p data-testid="plain-job-section">plain section for {jobId}</p>
