package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ogpourya/vox/transcribe"
	"github.com/ogpourya/vox/tts"
)

const chunkDuration = 15.0
const maxConcurrentUploads = 10
const maxRetries = 10

// maxConcurrentFiles caps simultaneous files (each spawns an ffmpeg split).
const maxConcurrentFiles = 4

// maxChunkWait bounds total backoff per chunk so sustained quota errors
// fail in minutes, not tens of minutes.
const maxChunkWait = 2 * time.Minute

// uploadSem caps concurrent transcription uploads process-wide
// (a per-file semaphore would multiply the cap by the file count).
var uploadSem = make(chan struct{}, maxConcurrentUploads)

// fileSem caps concurrent files for the same reason.
var fileSem = make(chan struct{}, maxConcurrentFiles)

func main() {
	lang := flag.String("lang", "en-US", "Language code (e.g. en-US, fr, es)")
	voice := flag.String("voice", "en-US-Casual-K", "TTS voice name")
	rate := flag.Float64("rate", 1.0, "TTS speaking rate (0.25-4.0)")
	out := flag.String("o", "output.mp3", "TTS output file (saves instead of playing)")
	debug := flag.Bool("debug", false, "Debug mode - show progress and errors")
	help := flag.Bool("help", false, "Show help")
	flag.Parse()

	// Signal-aware context: Ctrl-C stops new work and aborts in-flight
	// ffmpeg/uploads/sleeps instead of abandoning temp files.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *help {
		printHelp()
		return
	}

	if !isStdinPiped() {
		// No stdin: plain text means TTS, audio files mean STT.
		if flag.NArg() > 0 && !isSTTInput(flag.Args()) {
			if !explicitFlag("lang") {
				*lang = langFromVoice(*voice)
			}
			runTTS(ctx, strings.Join(flag.Args(), " "), *voice, *lang, *out, *rate, explicitFlag("o"), *debug)
			return
		}
	}

	checkFFmpegAndProbe()

	files := getFilesFromArgsOrStdin()
	if len(files) == 0 {
		printHelp()
		return
	}
	files = uniqueFiles(files)

	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Error: file not found: %s\n", f)
			} else {
				fmt.Fprintf(os.Stderr, "Error: cannot access file %s: %v\n", f, err)
			}
			os.Exit(1)
		}
	}

	type fileResult struct {
		key  string
		text *string
		err  error
	}

	fileResultsChan := make(chan fileResult, len(files))
	var wg sync.WaitGroup

	startTotal := time.Now()
	keys := resultKeys(files)

	for i, file := range files {
		wg.Add(1)
		go func(idx int, f string) {
			defer wg.Done()
			select {
			case fileSem <- struct{}{}:
				defer func() { <-fileSem }()
			case <-ctx.Done():
				fileResultsChan <- fileResult{key: keys[idx], err: ctx.Err()}
				return
			}
			if *debug {
				fmt.Fprintf(os.Stderr, "🚀 Starting file: %s\n", f)
			}

			text, err := processFileFast(ctx, f, *lang, *debug)

			fileResultsChan <- fileResult{
				key:  keys[idx],
				text: text,
				err:  err,
			}
		}(i, file)
	}

	wg.Wait()
	close(fileResultsChan)

	if *debug {
		fmt.Fprintf(os.Stderr, "✅ Total time taken: %v\n", time.Since(startTotal))
	}

	results := make(map[string]*string)
	var failed []string
	for res := range fileResultsChan {
		results[res.key] = res.text
		if res.err != nil {
			failed = append(failed, res.key)
			fmt.Fprintf(os.Stderr, "Error: %s: %v\n", res.key, res.err)
		}
	}

	printJSON(results)

	if len(failed) > 0 {
		os.Exit(1)
	}
}

func processFileFast(ctx context.Context, file, lang string, debug bool) (*string, error) {
	tmpDir, err := os.MkdirTemp("", "vox_chunks_*")
	if err != nil {
		return nil, fmt.Errorf("failed to make temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	chunkPattern := filepath.Join(tmpDir, "chunk_%05d.wav")

	if debug {
		fmt.Fprintf(os.Stderr, "🔪 Splitting audio %s...\n", file)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-i", file,
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%f", chunkDuration),
		"-c:a", "pcm_s16le",
		"-ar", "16000",
		"-ac", "1",
		chunkPattern,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg splitting failed: %s", string(output))
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list chunks: %w", err)
	}
	var chunks []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".wav") {
			chunks = append(chunks, filepath.Join(tmpDir, e.Name()))
		}
	}
	sort.Strings(chunks)

	if len(chunks) == 0 {
		return nil, fmt.Errorf("no audio chunks created")
	}

	type chunkResult struct {
		index int
		text  string
		err   error
	}

	resultsChan := make(chan chunkResult, len(chunks))
	var chunkWg sync.WaitGroup

	for i, chunkPath := range chunks {
		chunkWg.Add(1)

		go func(idx int, path string) {
			defer chunkWg.Done()
			select {
			case uploadSem <- struct{}{}:
				defer func() { <-uploadSem }()
			case <-ctx.Done():
				resultsChan <- chunkResult{index: idx, err: ctx.Err()}
				return
			}

			var txt *string
			var err error
			var waited time.Duration

		retry:
			for attempt := 1; attempt <= maxRetries; attempt++ {
				// Chunks are already 16kHz mono: no redundant re-encode.
				txt, err = transcribe.TranscribeWAV(ctx, path, lang)
				if err == nil {
					break
				}

				// Permanent failures (bad lang, bad audio) fail fast.
				if !transcribe.IsRetryable(err) {
					if debug {
						fmt.Fprintf(os.Stderr, "❌ Chunk %d failed (not retrying): %v\n", idx, err)
					}
					break
				}

				if attempt == maxRetries {
					break
				}
				wait := time.Second * time.Duration(attempt)
				var apiErr *transcribe.APIError
				if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
					wait = min(apiErr.RetryAfter, time.Minute)
				}
				// Jitter so concurrent chunks don't hammer the shared key in lockstep.
				wait = wait/2 + time.Duration(rand.Int63n(int64(wait/2)+1))
				if waited+wait > maxChunkWait {
					if debug {
						fmt.Fprintf(os.Stderr, "❌ Chunk %d giving up after %v of backoff: %v\n", idx, waited, err)
					}
					break
				}
				if debug {
					fmt.Fprintf(os.Stderr, "🔄 Retry %d/%d for chunk %d in %v: %v\n", attempt+1, maxRetries, idx, wait, err)
				}

				select {
				case <-ctx.Done():
					err = ctx.Err()
					break retry
				case <-time.After(wait):
					waited += wait
				}
			}

			res := chunkResult{index: idx, err: err}
			if txt != nil {
				res.text = *txt
			}
			resultsChan <- res
		}(i, chunkPath)
	}

	chunkWg.Wait()
	close(resultsChan)

	orderedText := make([]string, len(chunks))
	var errs []string
	var firstErr error

	for res := range resultsChan {
		if res.err != nil {
			errs = append(errs, fmt.Sprintf("chunk %d", res.index))
			if firstErr == nil {
				firstErr = res.err
			}
			// We just leave this index empty in orderedText
		} else {
			orderedText[res.index] = res.text
		}
	}

	if len(errs) == len(chunks) {
		return nil, fmt.Errorf("failed to transcribe all %d chunks: %v", len(chunks), firstErr)
	}

	// Partial failures return what succeeded; total failure errors out above.
	if len(errs) > 0 && debug {
		log.Printf("⚠️ Warning: Failed to transcribe chunks: %v (skipping them)", errs)
	}

	fullText := joinOrdered(orderedText)
	return &fullText, nil
}

func checkFFmpegAndProbe() {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "Error: ffmpeg not found. Install and add to PATH.")
		os.Exit(1)
	}
}

func isStdinPiped() bool {
	stdinInfo, err := os.Stdin.Stat()
	return err == nil && (stdinInfo.Mode()&os.ModeCharDevice) == 0
}

func explicitFlag(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// langFromVoice derives the language code from a voice name
// (e.g. en-IN-Chirp-HD-D -> en-IN). Falls back to en-US.
func langFromVoice(voice string) string {
	parts := strings.Split(voice, "-")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}

// isSTTInput reports whether args look like audio files (existing regular
// file or audio extension) rather than text to speak. Directories never
// count: they cannot be transcribed.
func isSTTInput(args []string) bool {
	audioExts := []string{".mp3", ".wav", ".ogg", ".oga", ".m4a", ".aac", ".flac", ".opus", ".wma", ".aiff", ".aif", ".webm", ".mp4"}
	for _, a := range args {
		if strings.TrimSpace(a) == "" {
			continue
		}
		lower := strings.ToLower(a)
		for _, ext := range audioExts {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
		if info, err := os.Stat(a); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func runTTS(ctx context.Context, text, voice, lang, out string, rate float64, save, debug bool) {
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(os.Stderr, "Error: no text supplied.")
		os.Exit(1)
	}
	if rate < 0.25 || rate > 4.0 {
		fmt.Fprintln(os.Stderr, "Error: rate must be between 0.25 and 4.0.")
		os.Exit(1)
	}

	audio, err := tts.Synthesize(ctx, text, voice, lang, rate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Save mode (-o given): write file, no playback.
	if save {
		if err := os.WriteFile(out, audio, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to write %s: %v\n", out, err)
			os.Exit(1)
		}
		fmt.Printf("Voice : %s\nCreated: %s\n", voice, out)
		return
	}

	// Play mode (no -o): temp file, play, clean up.
	tmp, err := os.CreateTemp("", "vox_play_*.mp3")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(audio); err != nil {
		tmp.Close()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	tmp.Close()

	if debug {
		fmt.Printf("Voice : %s\n", voice)
	}
	playAudio(ctx, tmpName, debug)
}

func playAudio(ctx context.Context, file string, debug bool) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("ffplay"); err == nil {
		cmd = exec.CommandContext(ctx, "ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", file)
	} else if _, err := exec.LookPath("mpv"); err == nil {
		cmd = exec.CommandContext(ctx, "mpv", "--no-video", file)
	} else {
		fmt.Fprintln(os.Stderr, "Warning: ffplay/mpv not found. Audio will not be played.")
		return
	}
	if debug {
		fmt.Println("Playing...")
	}
	if err := cmd.Run(); err != nil && debug {
		fmt.Fprintf(os.Stderr, "Warning: playback failed: %v\n", err)
	}
}

func getFilesFromArgsOrStdin() []string {
	piped := isStdinPiped()
	var files []string

	if flag.NArg() > 0 && piped {
		fmt.Fprintln(os.Stderr, "Warning: stdin is piped; ignoring command-line arguments.")
	}

	if piped {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				files = append(files, line)
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error reading stdin: %v\n", err)
		}
	} else {
		if flag.NArg() == 0 {
			return nil
		}
		for _, a := range flag.Args() {
			if strings.TrimSpace(a) != "" {
				files = append(files, a)
			}
		}
	}
	return files
}

// resultKeys maps each input file to its JSON output key: the basename,
// unless basenames collide, in which case the full path keeps both results.
func resultKeys(files []string) []string {
	counts := make(map[string]int)
	for _, f := range files {
		counts[filepath.Base(f)]++
	}
	keys := make([]string, len(files))
	for i, f := range files {
		if counts[filepath.Base(f)] > 1 {
			keys[i] = f
		} else {
			keys[i] = filepath.Base(f)
		}
	}
	return keys
}

// joinOrdered joins per-chunk texts, skipping empty slots left by silent
// or failed chunks so no double spaces appear.
func joinOrdered(texts []string) string {
	parts := make([]string, 0, len(texts))
	for _, t := range texts {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func uniqueFiles(files []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, f := range files {
		clean := filepath.Clean(f)
		if !seen[clean] {
			seen[clean] = true
			unique = append(unique, clean)
		}
	}
	return unique
}

func printJSON(data map[string]*string) {
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal results: %v", err)
	}
	fmt.Println(string(out))
}

func printHelp() {
	fmt.Println(`vox - Speech to text, text to speech

Usage:
  vox [options] file1 file2 ...
        Transcribe audio files to JSON
  vox [options] "text to speak"
        Synthesize speech to an audio file

Options:
  -lang string
        Language code (STT default "en-US"; TTS default matches -voice)
  -voice string
        TTS voice name (default "en-US-Casual-K")
        Voices: https://docs.cloud.google.com/text-to-speech/docs/list-voices-and-types
  -rate float
        TTS speaking rate 0.25-4.0 (default 1.0)
  -o string
        TTS output file (saves instead of playing, default "output.mp3")
  -debug
        Show detailed progress
  -help
        Show help`)
}
