#!/bin/sh
# Builds the Windows zip and Linux tarball into dist/.
# Needs Go. For Windows it also needs vendor-dl/ffmpeg-win.zip
# (FFmpeg "release essentials" build from https://www.gyan.dev/ffmpeg/builds/).
set -e
cd "$(dirname "$0")"
rm -rf dist
mkdir -p dist/VideoMerger-Windows dist/VideoMerger-Linux

GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist/VideoMerger-Windows/VideoMerger.exe .
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/VideoMerger-Linux/VideoMerger .

unzip -jq vendor-dl/ffmpeg-win.zip '*/bin/ffmpeg.exe' -d dist/VideoMerger-Windows
unzip -p vendor-dl/ffmpeg-win.zip '*/LICENSE' > dist/VideoMerger-Windows/FFmpeg-LICENSE.txt
cp packaging/windows.txt "dist/VideoMerger-Windows/How to use.txt"
cp packaging/linux.txt "dist/VideoMerger-Linux/How to use.txt"

cd dist
zip -qr9 VideoMerger-Windows.zip VideoMerger-Windows
tar czf VideoMerger-Linux.tar.gz VideoMerger-Linux
ls -la *.zip *.tar.gz
