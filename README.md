# vox

Speech-to-text and text-to-speech in one CLI. No API key required.

Transcription uses a free Google Speech-to-Text endpoint; accuracy depends on it.

## Features

- Audio → text (JSON to stdout), single file, batch, or stdin
- Text → speech (MP3 file, auto-plays via `ffplay`/`mpv` when available)
- Long audio split into chunks and transcribed concurrently
- Configurable language (`-lang`, default `en-US`) and voice (`-voice`, default `en-US-Casual-K`)

## Install

Requires `ffmpeg` in `PATH`:

```bash
sudo apt install ffmpeg
GOPROXY=direct go install github.com/ogpourya/vox@latest
```

## Usage

```bash
vox file1.mp3 file2.ogg          # transcribe to JSON
cat filelist.txt | vox           # transcribe batch from stdin
vox "hey there buddy"            # speak, save to output.mp3 and play
vox -o speech.mp3 -no-play "hi"  # speak, save only
```

Options: `-lang`, `-voice`, `-o`, `-no-play`, `-debug`, `-help`. Full help: `vox -help`.

Voices: https://docs.cloud.google.com/text-to-speech/docs/list-voices-and-types
Languages: https://cloud.google.com/speech-to-text/docs/languages
