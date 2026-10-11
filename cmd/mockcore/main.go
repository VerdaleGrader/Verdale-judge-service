// mockcore stands in for Core service during Milestone 1: it receives
// result callbacks from Judge, verifies their HMAC signature exactly as
// Core will, prints them and saves them under -out.
//
// -fail-first N answers 503 to the first N callbacks to exercise Judge's
// retry and outbox.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kanonkc/verdale-judge-service/configs"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
)

func main() {
	address := flag.String("addr", ":8090", "listen address")
	outDir := flag.String("out", "tmp/results", "directory where received results are saved")
	failFirst := flag.Int64("fail-first", 0, "answer 503 to the first N callbacks")
	flag.Parse()

	keyring, err := configs.ParseKeyring(os.Getenv("MOCKCORE_KEYS"))
	if err != nil {
		log.Fatalf("MOCKCORE_KEYS (the judge-to-core keyring, kid:secret): %v", err)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	verifier := hmacauth.Verifier{Keyring: keyring, Tolerance: 300 * time.Second}
	var calls atomic.Int64
	var seen sync.Map

	http.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	http.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
		if err != nil {
			http.Error(writer, "read body", http.StatusBadRequest)
			return
		}
		if _, err := verifier.Verify(request.Header, request.Method, request.URL.RequestURI(), body); err != nil {
			log.Printf("REJECTED %s %s: %v", request.Method, request.URL.RequestURI(), err)
			http.Error(writer, err.Error(), http.StatusUnauthorized)
			return
		}
		if call := calls.Add(1); call <= *failFirst {
			log.Printf("simulating an outage: answering 503 to callback #%d", call)
			http.Error(writer, "simulated outage", http.StatusServiceUnavailable)
			return
		}
		var result contract.JobResult
		if err := json.Unmarshal(body, &result); err != nil {
			http.Error(writer, "not a JobResult", http.StatusBadRequest)
			return
		}
		jobID := result.JobId.String()
		if _, duplicate := seen.LoadOrStore(jobID, true); duplicate {
			log.Printf("duplicate result for %s acknowledged (idempotent by job_id)", jobID)
			writer.WriteHeader(http.StatusOK)
			return
		}
		_ = os.WriteFile(filepath.Join(*outDir, jobID+".json"), prettyJSON(body), 0o644)
		log.Print(describe(result))
		writer.WriteHeader(http.StatusNoContent)
	})

	log.Printf("mock core listening on %s, saving results to %s", *address, *outDir)
	log.Fatal(http.ListenAndServe(*address, nil))
}

func describe(result contract.JobResult) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("RESULT %s kind=%s verdict=%s passed=%d/%d time=%dms memory=%dKB",
		result.JobId, result.Kind, result.Verdict, result.Summary.Passed, result.Summary.Total, result.Summary.TimeMs, result.Summary.MemoryKb))
	if result.Compile != nil && !result.Compile.Ok {
		lines = append(lines, "  compile error:\n    "+strings.ReplaceAll(strings.TrimSpace(result.Compile.Message), "\n", "\n    "))
	}
	if result.Error != nil {
		lines = append(lines, fmt.Sprintf("  system error %s: %s", result.Error.Code, result.Error.Message))
	}
	for _, test := range result.Tests {
		line := fmt.Sprintf("  test %-6s %-7s %5dms %7dKB", test.Id, test.Verdict, test.TimeMs, test.MemoryKb)
		if test.CheckerMessage != nil {
			line += "  " + *test.CheckerMessage
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func prettyJSON(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	pretty, _ := json.MarshalIndent(value, "", "  ")
	return append(pretty, '\n')
}
