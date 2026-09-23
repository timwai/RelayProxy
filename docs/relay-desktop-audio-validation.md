# Relay Desktop Windows Audio Validation

This checklist validates the real Windows audio path after the automated CI coverage:

WASAPI loopback capture -> Opus encode -> RD/1 network -> jitter/PLC -> Opus decode -> WASAPI render.

It deliberately does not change production bitrate or FEC defaults. Use the measured diagnostics first.

## Prerequisites

- Run current RelayProxy builds on two Windows machines.
- Use Relay Desktop, not native RDP, for the validation session.
- Leave audio enabled. Current negotiation prefers Opus when both peers advertise support and falls back to pcm_s16le for legacy peers.
- On the controlled machine, play continuous audio for at least 60 seconds so the diagnostics window contains enough traffic.

## Baseline run

1. Connect from the controller to the target.
2. Open the Windows native Relay Desktop viewer.
3. Confirm remote audio is audible and remains synchronized enough for normal use.
4. Keep playback active for 60-120 seconds.
5. In Remote Desktop, click 导出诊断.
6. Analyze the exported JSON:

    powershell -ExecutionPolicy Bypass -File .\scripts\analyze-desktop-audio.ps1 -Path .\relay-desktop-diagnostics-DEVICE-TIMESTAMP.json -RequireOpus

For a stricter repeatable baseline, add thresholds. These are test gates, not production defaults:

    powershell -ExecutionPolicy Bypass -File .\scripts\analyze-desktop-audio.ps1 -Path .\relay-desktop-diagnostics-DEVICE-TIMESTAMP.json -RequireOpus -MaxLossPercent 5 -MaxQueueDrops 0 -MaxGapSkippedFrames 0 -MaxPlayoutTimeouts 0 -MinCompressionRatio 8

The script exits with code 0 on pass, 1 when a requested threshold fails, and 2 when the diagnostics file is unusable or older than schema v5.

## Key fields

The exported audioValidation block is the primary summary:

- requestedCodec / codec verifies negotiation actually selected Opus.
- observedPayloadBitrate is the measured received audio payload bitrate over the sampled window.
- rawPcmBitrate / observedCompressionRatio compares compressed traffic with the equivalent PCM stream.
- estimatedNetworkLossPercent derives network loss from received frames plus PLC-concealed and large-gap-skipped frames.
- concealmentFrames counts short losses hidden by Opus PLC.
- gapSkippedFrames counts long loss bursts where playout deliberately jumps forward to preserve realtime behavior.
- queueDroppedFrames is local realtime queue overflow and is intentionally excluded from network-loss feedback.
- reorderedFrames, duplicateFrames, lateFrames and playoutTimeoutFrames separate network completion order from local playout pressure.
- pcmFallbackSamples should remain zero during an Opus validation run.

## Suggested matrix

| Case | Duration | What to watch |
| --- | ---: | --- |
| Clean LAN/P2P | 60-120 s | Opus active, no PCM fallback, low/no PLC, no queue drops |
| Relay path | 60-120 s | Payload bitrate remains near the negotiated target; no persistent queue growth |
| Normal WAN | 2-5 min | PLC may occur, but large-gap skips and queue drops should stay uncommon |
| While changing video quality/resolution | 60-120 s | Audio generation/codec remains stable and playback does not stall |
| Target audio stops/restarts | 30-60 s | Session remains healthy; no runaway queue or stale playback |

Do not enable FEC or change the default 96 kbps Opus target solely from synthetic tests. Compare multiple real-hardware reports first, especially Windows machines with different audio devices and Intel/NVIDIA/AMD graphics combinations.
