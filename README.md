# vox

Speech-to-text and text-to-speech in one CLI. No API key required.

`vox` transcribes audio files to JSON and synthesizes speech audio from text,
using free Google endpoints. Transcription accuracy depends entirely on the
Google Speech-to-Text API.

## Features

- Audio → text: JSON to stdout, single file, batch, or file list via stdin
- Text → speech: plays audio directly, or saves MP3 with `-o`
- Long audio is split into chunks and transcribed concurrently
- Configurable language (`-lang`), voice (`-voice`), and speaking rate (`-rate`)

## Install

Requires `ffmpeg` in `PATH`:

```bash
sudo apt install ffmpeg
GOPROXY=direct go install github.com/ogpourya/vox@latest
```

Prebuilt Linux binaries are attached to the rolling
[latest release](https://github.com/ogpourya/vox/releases/tag/latest).

## Usage

Transcribe audio (speech-to-text):

```bash
vox talk.mp3                         # transcribe one file to JSON
vox -lang fr-FR talk.mp3 talk.ogg    # transcribe with language
vox a.mp3 b.mp3                      # transcribe multiple files
cat filelist.txt | vox               # transcribe batch from stdin
```

Speak text (text-to-speech):

```bash
vox "hey there buddy"                          # play, nothing saved
vox -o speech.mp3 "hey there buddy"            # save MP3, no playback
vox -voice en-IN-Chirp-HD-D "hello"            # different voice
vox -rate 1.5 "a bit faster"                   # speaking rate 0.25-4.0
```

How input is interpreted: piped stdin is always a file list. Otherwise,
arguments that are existing files or have an audio extension are transcribed;
anything else is spoken as text.

## Options

| Flag      | Default           | Description                                        |
|-----------|-------------------|----------------------------------------------------|
| `-lang`   | matches `-voice`  | Language code, e.g. `en-US`, `fr-FR`               |
| `-voice`  | `en-US-Casual-K`  | TTS voice name                                     |
| `-rate`   | `1.0`             | TTS speaking rate, `0.25`–`4.0`                    |
| `-o`      | `output.mp3`      | TTS output file (saves instead of playing)         |
| `-debug`  | off               | Show progress and error messages                   |
| `-help`   | —                 | Show help message                                  |

Playback uses `ffplay` or `mpv` when available; otherwise audio is not played
and a warning is printed.

## Reference

- TTS voices: https://docs.cloud.google.com/text-to-speech/docs/list-voices-and-types
- STT languages: https://cloud.google.com/speech-to-text/docs/languages

## Notes

- No API key is required; both directions share a built-in key with rate
  limits. Heavy use may return quota errors — retry later.
- Tests: `./test.sh` (requires network access for the Google endpoints).
