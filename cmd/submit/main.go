// submit sends a signed job to Judge, the way Core will. It is the
// Milestone 1 driver: it uploads the problem's test files into the local
// storage directory (named by SHA-256, like the R2 test bucket), builds
// the JobRequest and POSTs it.
//
//	go run ./cmd/submit -problem examples/aplusb -source examples/aplusb/solutions/ac.cpp
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
)

// problem.json in a problem directory.
type problemFile struct {
	TimeMs   int      `json:"time_ms"`
	MemoryKb int      `json:"memory_kb"`
	OutputKb *int     `json:"output_kb,omitempty"`
	Samples  []string `json:"samples"`
}

func main() {
	problemDir := flag.String("problem", "examples/aplusb", "problem directory with problem.json and tests/*.in, *.out")
	sourcePath := flag.String("source", "", "solution file; the language comes from its extension (.c, .cpp, .py)")
	kind := flag.String("kind", "submission", "submission (stop at first failure) or verify (run every test)")
	judgeURL := flag.String("judge", "http://localhost:8081", "Judge base URL")
	callbackURL := flag.String("callback", "http://mockcore:8090/internal/judge/results", "where Judge sends the result")
	storageDir := flag.String("storage", "dev/storage/tests", "local test-file storage (the judge's storage.local_dir + key_prefix)")
	jobID := flag.String("job-id", "", "job id (default: a new UUID); reuse one to see idempotency")
	flag.Parse()

	if *sourcePath == "" {
		log.Fatal("-source is required")
	}
	keyID, secret := os.Getenv("SUBMIT_KEY_ID"), os.Getenv("SUBMIT_SECRET")
	if keyID == "" || secret == "" {
		log.Fatal("SUBMIT_KEY_ID and SUBMIT_SECRET must hold a core-to-judge key (see .env.example)")
	}

	request, err := buildJob(*problemDir, *sourcePath, contract.JobKind(*kind), *callbackURL, *storageDir)
	if err != nil {
		log.Fatal(err)
	}
	if *jobID != "" {
		parsed, err := uuid.Parse(*jobID)
		if err != nil {
			log.Fatalf("-job-id: %v", err)
		}
		request.JobId = parsed
	}
	body, _ := json.Marshal(request)

	httpRequest, _ := http.NewRequest(http.MethodPost, strings.TrimRight(*judgeURL, "/")+"/internal/jobs", bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	hmacauth.Signer{KeyID: keyID, Secret: []byte(secret)}.SignRequest(httpRequest, "/internal/jobs", body)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(httpRequest)
	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(response.Body)
	fmt.Printf("job %s (%s, %s, %d tests) -> %s\n%s\n", request.JobId, request.Language, request.Kind, len(request.Tests), response.Status, answer)
	if response.StatusCode != http.StatusAccepted {
		os.Exit(1)
	}
}

func buildJob(problemDir, sourcePath string, kind contract.JobKind, callbackURL, storageDir string) (contract.JobRequest, error) {
	var problem problemFile
	raw, err := os.ReadFile(filepath.Join(problemDir, "problem.json"))
	if err != nil {
		return contract.JobRequest{}, err
	}
	if err := json.Unmarshal(raw, &problem); err != nil {
		return contract.JobRequest{}, fmt.Errorf("problem.json: %w", err)
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return contract.JobRequest{}, err
	}
	language, err := languageOf(sourcePath)
	if err != nil {
		return contract.JobRequest{}, err
	}

	inputs, err := filepath.Glob(filepath.Join(problemDir, "tests", "*.in"))
	if err != nil || len(inputs) == 0 {
		return contract.JobRequest{}, fmt.Errorf("no tests/*.in in %s", problemDir)
	}
	sort.Slice(inputs, func(left, right int) bool { return naturalLess(inputs[left], inputs[right]) })
	samples := map[string]bool{}
	for _, id := range problem.Samples {
		samples[id] = true
	}
	var tests []contract.TestCase
	for _, inputPath := range inputs {
		id := strings.TrimSuffix(filepath.Base(inputPath), ".in")
		input, err := upload(inputPath, storageDir)
		if err != nil {
			return contract.JobRequest{}, err
		}
		output, err := upload(strings.TrimSuffix(inputPath, ".in")+".out", storageDir)
		if err != nil {
			return contract.JobRequest{}, err
		}
		sample := samples[id]
		tests = append(tests, contract.TestCase{Id: id, Input: input, Output: &output, Sample: &sample})
	}
	return contract.JobRequest{
		JobId:       uuid.New(),
		Kind:        kind,
		Language:    language,
		Source:      string(source),
		Limits:      contract.Limits{TimeMs: problem.TimeMs, MemoryKb: problem.MemoryKb, OutputKb: problem.OutputKb},
		Tests:       tests,
		CallbackUrl: callbackURL,
	}, nil
}

// upload copies a file into storage under its SHA-256, as Core will do
// with the R2 test bucket.
func upload(path, storageDir string) (contract.TestFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return contract.TestFile{}, err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return contract.TestFile{}, err
	}
	target := filepath.Join(storageDir, digest)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return contract.TestFile{}, err
		}
	}
	return contract.TestFile{Sha256: digest, SizeBytes: int64(len(content))}, nil
}

func languageOf(path string) (contract.Language, error) {
	switch filepath.Ext(path) {
	case ".c":
		return contract.LanguageC, nil
	case ".cpp", ".cc":
		return contract.LanguageCpp, nil
	case ".py":
		return contract.LanguagePython, nil
	}
	return "", fmt.Errorf("cannot tell the language of %s (use .c, .cpp or .py)", path)
}

// naturalLess sorts tests/2.in before tests/10.in.
func naturalLess(left, right string) bool {
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}
