#!/bin/bash

set -e

echo "Starting tests..."

BIN="go run vox.go"
REPO="$(pwd)"

# TTS shares a public API key with rate limits: retry once on 429.
tts_save() {
  local out="$1"; shift
  local attempt=0
  local output
  while [[ $attempt -lt 3 ]]; do
    output=$($BIN -o "$out" "$@" 2>&1) && return 0
    [[ "$output" == *"429"* ]] || { echo "$output" >&2; return 1; }
    attempt=$((attempt + 1))
    echo "TTS quota hit, retrying ($attempt/3)..." >&2
    sleep 15
  done
  echo "$output" >&2
  return 1
}

# Test 1: No files given => should show help
echo "Test 1: No files"
if $BIN 2>&1 | grep -q "Usage"; then
  echo "Passed: Help shown when no files provided"
else
  echo "Failed: Help not shown"
  exit 1
fi

# Test 2: File not found => should error and exit
echo "Test 2: Missing file"
if $BIN audios/nonexistent.mp3 2>&1 | grep -q "file not found"; then
  echo "Passed: Missing file error detected"
else
  echo "Failed: Missing file error not detected"
  exit 1
fi

# Test 3: Single valid file
echo "Test 3: Single file"
output=$($BIN -debug audios/sample1.mp3)
if echo "$output" | grep -q '"sample1.mp3":'; then
  echo "Passed: Single file transcription output detected"
else
  echo "Failed: Single file transcription output missing"
  exit 1
fi

# Test 4: Multiple files (one valid, one missing)
echo "Test 4: Multiple files with missing"
set +e
output=$($BIN audios/sample1.mp3 audios/missing.mp3 2>&1)
exit_code=$?
set -e
if [[ $exit_code -ne 0 && "$output" == *"file not found"* ]]; then
  echo "Passed: Error on missing file with multiple inputs"
else
  echo "Failed: Missing file not handled correctly with multiple inputs"
  exit 1
fi

# Test 5: Multiple valid files
echo "Test 5: Multiple valid files"
output=$($BIN -lang en-US audios/sample1.mp3 audios/sample2.mp3)
if echo "$output" | grep -q '"sample1.mp3":' && echo "$output" | grep -q '"sample2.mp3":'; then
  echo "Passed: Multiple files transcription output detected"
else
  echo "Failed: Multiple files transcription output missing or incomplete"
  exit 1
fi

# Test 6: Reading from stdin (list of files)
echo "Test 6: Input from stdin"
echo -e "audios/sample1.mp3\naudios/sample2.mp3" | $BIN -debug | grep -q '"sample1.mp3":'
if [ $? -eq 0 ]; then
  echo "Passed: Reading files from stdin works"
else
  echo "Failed: Reading files from stdin failed"
  exit 1
fi

# Test 7: TTS synthesis + STT round-trip
echo "Test 7: TTS round-trip"
TTS_OUT="$(mktemp /tmp/opencode/vox_tts_XXXXXX.mp3)"
tts_save "$TTS_OUT" 'hey there buddy' > /dev/null
if [[ ! -s "$TTS_OUT" ]]; then
  echo "Failed: TTS output file empty or missing"
  exit 1
fi
output=$($BIN "$TTS_OUT")
rm -f "$TTS_OUT"
if echo "$output" | grep -iq "hey there buddy"; then
  echo "Passed: TTS round-trip transcription matches"
else
  echo "Failed: TTS round-trip mismatch: $output"
  exit 1
fi

# Test 8: TTS save mode writes valid audio
echo "Test 8: TTS save mode"
TTS_OUT="$(mktemp /tmp/opencode/vox_tts_XXXXXX.mp3)"
tts_save "$TTS_OUT" 'testing one two three' > /dev/null
if [[ ! -s "$TTS_OUT" ]]; then
  echo "Failed: TTS save produced empty file"
  rm -f "$TTS_OUT"
  exit 1
fi
if ! ffprobe -v error -show_entries format=format_name -of csv=p=0 "$TTS_OUT" | grep -q "mp3"; then
  echo "Failed: TTS save is not valid MP3"
  rm -f "$TTS_OUT"
  exit 1
fi
rm -f "$TTS_OUT"
echo "Passed: TTS save writes valid MP3"

# Test 9: TTS play mode saves no file
echo "Test 9: TTS play mode"
PLAY_DIR="$(mktemp -d)"
set +e
(cd "$PLAY_DIR" && go -C "$REPO" run vox.go 'hello play mode' > /dev/null 2>&1)
exit_code=$?
set -e
if [[ $exit_code -ne 0 ]]; then
  echo "Failed: play mode exited $exit_code"
  rm -rf "$PLAY_DIR"
  exit 1
fi
if ls "$PLAY_DIR"/*.mp3 >/dev/null 2>&1; then
  echo "Failed: play mode left audio files behind"
  rm -rf "$PLAY_DIR"
  exit 1
fi
rm -rf "$PLAY_DIR"
echo "Passed: play mode saves no file"

# Test 10: TTS non-default voice (lang derived from voice)
echo "Test 10: TTS non-default voice"
TTS_OUT="$(mktemp /tmp/opencode/vox_tts_XXXXXX.mp3)"
tts_save "$TTS_OUT" -voice en-IN-Chirp-HD-D 'hello there friend' > /dev/null
if [[ ! -s "$TTS_OUT" ]]; then
  echo "Failed: non-default voice synthesis failed"
  rm -f "$TTS_OUT"
  exit 1
fi
rm -f "$TTS_OUT"
echo "Passed: non-default voice synthesis works"

# Test 11: TTS explicit -lang with matching voice
echo "Test 11: TTS explicit lang"
TTS_OUT="$(mktemp /tmp/opencode/vox_tts_XXXXXX.mp3)"
tts_save "$TTS_OUT" -lang en-IN -voice en-IN-Chirp-HD-D 'namaste' > /dev/null
if [[ ! -s "$TTS_OUT" ]]; then
  echo "Failed: explicit lang synthesis failed"
  rm -f "$TTS_OUT"
  exit 1
fi
rm -f "$TTS_OUT"
echo "Passed: explicit lang synthesis works"

# Test 12: TTS custom speaking rate
echo "Test 12: TTS speaking rate"
TTS_OUT="$(mktemp /tmp/opencode/vox_tts_XXXXXX.mp3)"
tts_save "$TTS_OUT" -rate 1.5 'speaking faster now' > /dev/null
if [[ ! -s "$TTS_OUT" ]]; then
  echo "Failed: custom rate synthesis failed"
  rm -f "$TTS_OUT"
  exit 1
fi
rm -f "$TTS_OUT"
set +e
output=$($BIN -o /tmp/opencode/vox_bad.mp3 -rate 9 'hi' 2>&1)
exit_code=$?
set -e
if [[ $exit_code -eq 0 || "$output" != *"rate"* ]]; then
  echo "Failed: out-of-range rate should error"
  exit 1
fi
echo "Passed: speaking rate works and validates range"

# Test 13: TTS invalid voice errors out
echo "Test 13: TTS invalid voice"
set +e
output=$($BIN -o /tmp/opencode/vox_bad.mp3 -voice bogus-voice-XYZ 'hi' 2>&1)
exit_code=$?
set -e
if [[ $exit_code -eq 0 || "$output" != *"Error"* ]]; then
  echo "Failed: invalid voice should error"
  exit 1
fi
echo "Passed: invalid voice errors correctly"

# Test 14: TTS empty text errors out
echo "Test 14: TTS empty text"
set +e
$BIN "" > /dev/null 2>&1
exit_code=$?
set -e
if [[ $exit_code -eq 0 ]]; then
  echo "Failed: empty text should error"
  exit 1
fi
echo "Passed: empty text errors correctly"

# Test 15: STT output is valid JSON
echo "Test 15: STT JSON validity"
output=$($BIN audios/sample1.mp3)
if ! echo "$output" | jq -e '."sample1.mp3" | type == "string"' > /dev/null; then
  echo "Failed: STT output is not valid JSON with expected key"
  exit 1
fi
echo "Passed: STT output is valid JSON"

# Test 16: Duplicate file args deduped
echo "Test 16: Duplicate files"
output=$($BIN audios/sample1.mp3 audios/sample1.mp3)
count=$(echo "$output" | grep -c '"sample1.mp3":')
if [[ "$count" -ne 1 ]]; then
  echo "Failed: duplicate file transcribed $count times"
  exit 1
fi
echo "Passed: duplicate files deduped"

# Test 17: -help exits zero with usage
echo "Test 17: Help flag"
if $BIN -help 2>&1 | grep -q "Usage"; then
  echo "Passed: -help shows usage"
else
  echo "Failed: -help broken"
  exit 1
fi

echo "All tests passed!"
