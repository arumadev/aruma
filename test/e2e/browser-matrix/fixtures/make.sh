#!/usr/bin/env bash
# Makes the media fixtures of the browser matrix with ffmpeg. They are committed, so a run needs
# no ffmpeg: a 4 s clip with a keyframe every half second, so that a seek lands without decoding
# from the start, as H.264 in MP4 (WebKit, Firefox; Playwright's Chromium has no proprietary
# codecs) and as VP9 in WebM (Chromium, Firefox), and 4 s of MP3. A few KiB each.
set -euo pipefail
cd "$(dirname "$0")"
src=(-f lavfi -i "testsrc=size=160x120:rate=10:duration=4")
ffmpeg -hide_banner -loglevel error -y "${src[@]}" -c:v libx264 -profile:v baseline -pix_fmt yuv420p \
  -g 5 -movflags +faststart -an clip.mp4
ffmpeg -hide_banner -loglevel error -y "${src[@]}" -c:v libvpx-vp9 -b:v 100k -g 5 -an clip.webm
ffmpeg -hide_banner -loglevel error -y -f lavfi -i "sine=frequency=440:duration=4" -c:a libmp3lame -b:a 32k song.mp3
ls -l clip.mp4 clip.webm song.mp3
