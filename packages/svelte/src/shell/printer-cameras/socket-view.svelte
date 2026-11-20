<!--
  One camera's H.264 live view over the live socket, for a browser that could
  not reach the server over WebRTC. It connects when it is drawn and hangs up
  when it goes, as the WebRTC view does.

  Each message is one picture, which WebCodecs decodes and the canvas draws.
  The decoder is configured from the first keyframe, which the server always
  sends first. A decoder that falls behind drops pictures until the next
  keyframe rather than drawing the past.
-->
<script lang="ts">
  import { onMount } from 'svelte';

  import { codecOf, pictureOf } from './h264';

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

  // How many pictures may wait in the decoder before the ones that are not
  // keyframes are dropped: about a second at the frame rates cameras run at.
  const BEHIND = 30;

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

    const decoder = new VideoDecoder({
      output: (frame) => {
        const target = canvas;
        const drawing = target?.getContext('2d');
        if (target !== undefined && drawing) {
          if (target.width !== frame.displayWidth) target.width = frame.displayWidth;
          if (target.height !== frame.displayHeight) {
            target.height = frame.displayHeight;
          }
          drawing.drawImage(frame, 0, 0);
        }
        frame.close();
      },
      error: fail,
    });

    let configured = false;
    let skipping = false;

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
      if (picture === undefined) return;

      if (!configured) {
        if (!picture.key) return;
        const codec = codecOf(picture.unit);
        if (codec === undefined) {
          fail();

          return;
        }
        // No description, so the decoder reads the pictures as Annex B.
        decoder.configure({ codec, optimizeForLatency: true });
        configured = true;
      }

      if (decoder.decodeQueueSize > BEHIND) skipping = true;
      if (skipping && !picture.key) return;
      skipping = false;

      try {
        decoder.decode(
          new EncodedVideoChunk({
            type: picture.key ? 'key' : 'delta',
            timestamp: Math.round(performance.now() * 1000),
            data: picture.unit,
          }),
        );
      } catch {
        fail();
      }
    });
    socket.addEventListener('close', fail);
    socket.addEventListener('error', fail);

    return () => {
      ended = true;
      socket.close();
      if (decoder.state !== 'closed') decoder.close();
    };
  });
</script>

<!-- A canvas is not an image a screen reader names, so the label sits on a
     wrapper that is. -->
<div class="absolute inset-0" role="img" aria-label={label}>
  <canvas bind:this={canvas} class="size-full object-contain"></canvas>
</div>
