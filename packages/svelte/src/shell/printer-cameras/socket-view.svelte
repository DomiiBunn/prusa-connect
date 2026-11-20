<!--
  One camera's H.264 live view over the live socket, for a browser that could
  not reach the server over WebRTC. It connects when it is drawn and hangs up
  when it goes, as the WebRTC view does.

  Each message is one picture, which WebCodecs decodes and the canvas draws.
  A picture that does not decode costs the pictures up to the next keyframe,
  not the view (live-decoder.ts).
-->
<script lang="ts">
  import { onMount } from 'svelte';

  import { pictureOf } from './h264';
  import { LiveDecoder } from './live-decoder';

  let {
    open,
    label,
    onFail,
  }: {
    /** Opens the live socket. */
    open: () => WebSocket;
    /** What a screen reader says the video is. */
    label: string;
    /** Called once when the live view could not start, or stopped. */
    onFail: () => void;
  } = $props();

  let canvas = $state<HTMLCanvasElement | undefined>(undefined);

  const draw = (frame: VideoFrame): void => {
    const target = canvas;
    const drawing = target?.getContext('2d');
    if (target === undefined || !drawing) return;
    if (target.width !== frame.displayWidth) target.width = frame.displayWidth;
    if (target.height !== frame.displayHeight) {
      target.height = frame.displayHeight;
    }
    drawing.drawImage(frame, 0, 0);
  };

  onMount(() => {
    let ended = false;
    let failed = false;
    const fail = (): void => {
      if (ended || failed) return;
      failed = true;
      onFail();
    };

    if (typeof VideoDecoder === 'undefined') {
      fail();

      return;
    }

    const decoder = new LiveDecoder({
      open: (output, error) => new VideoDecoder({ output, error }),
      chunk: (init) => new EncodedVideoChunk(init),
      draw,
      fail,
    });

    let socket: WebSocket;
    try {
      socket = open();
    } catch {
      decoder.close();
      fail();

      return;
    }

    socket.addEventListener('message', (event: MessageEvent<unknown>) => {
      if (!(event.data instanceof ArrayBuffer)) return;
      const picture = pictureOf(event.data);
      if (picture !== undefined) decoder.take(picture, performance.now());
    });
    socket.addEventListener('close', fail);
    socket.addEventListener('error', fail);

    return () => {
      ended = true;
      socket.close();
      decoder.close();
    };
  });
</script>

<!-- A canvas is not an image a screen reader names, so the label sits on a
     wrapper that is. -->
<div class="absolute inset-0" role="img" aria-label={label}>
  <canvas bind:this={canvas} class="size-full object-contain"></canvas>
</div>
