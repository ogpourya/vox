package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ogpourya/vox/transcribe"
	"github.com/ogpourya/vox/tts"
)

const chunkDuration = 15.0 
const maxConcurrentUploads = 10 
const maxRetries = 10 

func main() {
	lang := flag.String("lang", "en-US", "Language code (e.g. en-US, fr, es)")
	voice := flag.String("voice", "en-US-Casual-K", "TTS voice name")
	out := flag.String("o", "output.mp3", "TTS output file")
	noPlay := flag.Bool("no-play", false, "Don't play TTS audio")
	debug := flag.Bool("debug", false, "Debug mode - show progress and errors")
	help := flag.Bool("help", false, "Show help")
	flag.Parse()

	if *help {
		printHelp()
		return
	}

	if !isStdinPiped() {
		// No stdin: plain text means TTS, audio files mean STT.
		if flag.NArg() > 0 && !isSTTInput(flag.Args()) {
			runTTS(strings.Join(flag.Args(), " "), *voice, *lang, *out, *noPlay, *debug)
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
		if _, err := os.Stat(f); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: file not found: %s\n", f)
			os.Exit(1)
		}
	}

	type fileResult struct {
		filename string
		text     *string
		err      error
	}

	fileResultsChan := make(chan fileResult, len(files))
	var wg sync.WaitGroup

	startTotal := time.Now()

	for _, file := range files {
		wg.Add(1)
		go func(f string) {
			defer wg.Done()
			if *debug {
				fmt.Printf("🚀 Starting file: %s\n", f)
			}

			// We ignore the error here so we always get the text
			text, _ := processFileFast(f, *lang, *debug)
			
			fileResultsChan <- fileResult{
				filename: filepath.Base(f),
				text:     text,
				err:      nil, // Force nil error so JSON is always generated
			}
		}(file)
	}

	wg.Wait()
	close(fileResultsChan)

	if *debug {
		fmt.Printf("✅ Total time taken: %v\n", time.Since(startTotal))
	}

	results := make(map[string]*string)
	for res := range fileResultsChan {
		results[res.filename] = res.text
	}

	printJSON(results)
}

func processFileFast(file, lang string, debug bool) (*string, error) {
	tmpDir, err := os.MkdirTemp("", "vox_chunks_*")
	if err != nil {
		return nil, fmt.Errorf("failed to make temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir) 

	chunkPattern := filepath.Join(tmpDir, "chunk_%03d.wav")
	
	if debug {
		fmt.Printf("🔪 Splitting audio %s...\n", file)
	}

	cmd := exec.Command("ffmpeg", 
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

	var chunks []string
	entries, _ := os.ReadDir(tmpDir)
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

	sem := make(chan struct{}, maxConcurrentUploads)

	for i, chunkPath := range chunks {
		chunkWg.Add(1)
		
		go func(idx int, path string) {
			defer chunkWg.Done()
			sem <- struct{}{} 
			defer func() { <-sem }()

			var txt *string
			var err error

			for attempt := 1; attempt <= maxRetries; attempt++ {
				// Only log retries if it's NOT the silence error
				// This keeps logs clean for expected silence
				txt, err = transcribe.Transcribe(path, lang)
				if err == nil {
					break
				}
				
				// If error is "no transcription", don't retry, just accept it's empty
				if strings.Contains(err.Error(), "no transcription") {
					err = nil 
					empty := ""
					txt = &empty
					break
				}

				if debug && attempt > 1 {
					fmt.Printf("🔄 Retry %d/%d for chunk %d: %v\n", attempt, maxRetries, idx, err)
				} 
				
				time.Sleep(time.Second * time.Duration(attempt))
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

	for res := range resultsChan {
		if res.err != nil {
			errs = append(errs, fmt.Sprintf("chunk %d", res.index))
			// We just leave this index empty in orderedText
		} else {
			orderedText[res.index] = res.text
		}
	}

	// CHANGED: We do NOT return error here anymore. 
	// We just log warnings and return whatever text we managed to get.
	if len(errs) > 0 && debug {
		log.Printf("⚠️ Warning: Failed to transcribe chunks: %v (skipping them)", errs)
	}

	fullText := strings.TrimSpace(strings.Join(orderedText, " "))
	return &fullText, nil
}

func checkFFmpegAndProbe() {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintln(os.Stderr, "Error: ffmpeg not found. Install and add to PATH.")
		os.Exit(1)
	}
}

func isStdinPiped() bool {
	stdinInfo, _ := os.Stdin.Stat()
	return (stdinInfo.Mode() & os.ModeCharDevice) == 0
}

func allFilesExist(args []string) bool {
	for _, a := range args {
		if _, err := os.Stat(a); os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// isSTTInput reports whether args look like audio files (existing path or
// audio extension) rather than text to speak.
func isSTTInput(args []string) bool {
	audioExts := []string{".mp3", ".wav", ".ogg", ".oga", ".m4a", ".aac", ".flac", ".opus", ".wma", ".aiff", ".aif", ".webm", ".mp4"}
	for _, a := range args {
		if _, err := os.Stat(a); err == nil {
			return true
		}
		lower := strings.ToLower(a)
		for _, ext := range audioExts {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
	}
	return false
}

func runTTS(text, voice, lang, out string, noPlay, debug bool) {
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(os.Stderr, "Error: no text supplied.")
		os.Exit(1)
	}

	audio, err := tts.Synthesize(text, voice, lang)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(out, audio, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("Voice : %s\nOutput: %s\nCreated: %s\n", voice, out, out)

	if noPlay {
		return
	}
	playAudio(out, debug)
}

func playAudio(file string, debug bool) {
	var cmd *exec.Cmd
	if _, err := exec.LookPath("ffplay"); err == nil {
		cmd = exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", file)
	} else if _, err := exec.LookPath("mpv"); err == nil {
		cmd = exec.Command("mpv", "--no-video", file)
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
	stdinInfo, _ := os.Stdin.Stat()
	var files []string

	if (stdinInfo.Mode() & os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				files = append(files, line)
			}
		}
	} else {
		if flag.NArg() == 0 {
			return nil
		}
		files = flag.Args()
	}
	return files
}

func uniqueFiles(files []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
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
        Language code (default "en-US")
  -voice string
        TTS voice name (default "en-US-Casual-K")
        Voices: https://docs.cloud.google.com/text-to-speech/docs/list-voices-and-types
  -o string
        TTS output file (default "output.mp3")
  -no-play
        Don't play TTS audio
  -debug
        Show detailed progress
  -help
        Show help`)
}
