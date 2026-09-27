#!/bin/bash

set -e

echo "Starting tests..."

BIN="go run vox.go"

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
$BIN -o "$TTS_OUT" 'hey there buddy' > /dev/null
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

echo "All tests passed!"
