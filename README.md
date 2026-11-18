# octoconv-worker-av

AV worker for OctoConv. One process serves one approved manifest over Worker Protocol v2.

The process reads `AV_PROTOCOL_CORE_URL`, `AV_PROTOCOL_CLIENT_CERT`, `AV_PROTOCOL_CLIENT_KEY`, `AV_PROTOCOL_CLIENT_CA`, `AV_PROTOCOL_WORKER_ID`, and `AV_PROTOCOL_MANIFEST_PATH`. It refuses to start when `DATABASE_URL` or `REDIS_ADDR` is set. `AV_WORKER_CONCURRENCY` must be unset or `1`. Hello and pull advertise one slot, and one conversion runs at a time.

`schema/manifest-av-convert.json` is capability `octoconv.convert.av`. It names the thirty-five pairs: mov, avi, mkv, and webm to mp4, then mp4 to webm, then sources mp4, mov, avi, mkv, and webm each to mp3, wav, and m4a, then those sources each to jpg, png, and webp.

`ffmpeg` and `ffprobe` must be on `PATH`. `AV_THREADS` overrides the thread count passed to `ffmpeg`. The default engine budget is 753s (`AV_ENGINE_TIMEOUT`). Landlock confines both programs to the job directory. `AV_SANDBOX_DISABLED=true` turns that off. A sandbox setup failure exits 78. The manifest is not approved.

Parameters are `timecode`, `resolution_height`, and `codec`. An empty parameters object keeps the built-in defaults. An unknown key fails before either program runs.
