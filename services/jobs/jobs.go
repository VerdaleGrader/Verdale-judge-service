// Package jobs accepts judging jobs from Core: it validates every field and
// size, enforces idempotency by job_id, and enqueues the job.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	apperrors "github.com/kanonkc/verdale-judge-service/errors"
	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
)

// Limits from contracts/judge.openapi.yaml.
const (
	MaxSourceBytes   = 65536
	MinTimeMs        = 100
	MaxTimeMs        = 20000
	MinMemoryKb      = 16384
	MaxMemoryKb      = 2097152
	MinOutputKb      = 1
	MaxOutputKb      = 65536
	MinTests         = 1
	MaxTests         = 500
	MaxTestIDLength  = 64
	MaxCallbackBytes = 2048
)

var (
	testIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Service interface {
	Accept(ctx context.Context, request contract.JobRequest) (contract.JobAccepted, error)
	Get(ctx context.Context, jobID uuid.UUID) (contract.JobState, error)
}

type Config struct {
	AllowedCallbackPrefixes []string
	// MaxTestFileBytes is the largest test file Judge will download.
	MaxTestFileBytes int64
}

type service struct {
	store  jobstore.Repository
	config Config
	now    func() time.Time
}

func New(store jobstore.Repository, config Config) Service {
	return &service{store: store, config: config, now: time.Now}
}

func (s *service) Accept(ctx context.Context, request contract.JobRequest) (contract.JobAccepted, error) {
	if details := s.Validate(request); len(details) > 0 {
		return contract.JobAccepted{}, apperrors.Validation(details)
	}
	// Re-marshalling gives a canonical form, so the same job sent with
	// different whitespace or key order is still recognised as the same.
	canonical, err := json.Marshal(request)
	if err != nil {
		return contract.JobAccepted{}, fmt.Errorf("encode job: %w", err)
	}
	digest := sha256.Sum256(canonical)
	payloadHash := hex.EncodeToString(digest[:])

	state, created, err := s.store.Accept(ctx, request.JobId.String(), payloadHash, canonical, s.now())
	if err != nil {
		return contract.JobAccepted{}, apperrors.New(http.StatusServiceUnavailable, apperrors.CodeQueueUnavailable, "The job queue is unavailable. Retry later.")
	}
	if !created && state.PayloadSHA256 != payloadHash {
		return contract.JobAccepted{}, apperrors.New(http.StatusConflict, apperrors.CodeJobConflict,
			"A different job was already accepted with this job_id.")
	}
	return contract.JobAccepted{
		JobId:      request.JobId,
		Status:     state.Status,
		AcceptedAt: state.AcceptedAt,
		Duplicate:  !created,
	}, nil
}

func (s *service) Get(ctx context.Context, jobID uuid.UUID) (contract.JobState, error) {
	state, err := s.store.Get(ctx, jobID.String())
	if errors.Is(err, jobstore.ErrNotFound) {
		return contract.JobState{}, apperrors.New(http.StatusNotFound, apperrors.CodeJobNotFound, "No job with this job_id.")
	}
	if err != nil {
		return contract.JobState{}, apperrors.New(http.StatusServiceUnavailable, apperrors.CodeQueueUnavailable, "The job store is unavailable. Retry later.")
	}
	response := contract.JobState{
		JobId:      jobID,
		Status:     state.Status,
		AcceptedAt: state.AcceptedAt,
		UpdatedAt:  state.UpdatedAt,
	}
	if state.Verdict != "" {
		verdict := state.Verdict
		response.Verdict = &verdict
	}
	return response, nil
}

// Validate checks every field against the contract and returns one detail
// per problem.
func (s *service) Validate(request contract.JobRequest) []contract.ErrorDetail {
	var details []contract.ErrorDetail
	add := func(field, format string, args ...any) {
		details = append(details, contract.ErrorDetail{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if request.JobId == uuid.Nil {
		add("job_id", "is required")
	}
	if request.Kind != contract.JobKindSubmission && request.Kind != contract.JobKindVerify {
		add("kind", "must be submission or verify")
	}
	switch request.Language {
	case contract.LanguageC, contract.LanguageCpp, contract.LanguagePython:
	default:
		add("language", "must be c, cpp or python")
	}
	if request.Checker != nil && *request.Checker != contract.CheckerLine {
		add("checker", "must be line")
	}
	switch {
	case len(request.Source) == 0:
		add("source", "is required")
	case len(request.Source) > MaxSourceBytes:
		add("source", "is %d bytes; the limit is %d bytes", len(request.Source), MaxSourceBytes)
	case !utf8.ValidString(request.Source):
		add("source", "must be valid UTF-8")
	}

	limits := request.Limits
	if limits.TimeMs < MinTimeMs || limits.TimeMs > MaxTimeMs {
		add("limits.time_ms", "must be between %d and %d", MinTimeMs, MaxTimeMs)
	}
	if limits.MemoryKb < MinMemoryKb || limits.MemoryKb > MaxMemoryKb {
		add("limits.memory_kb", "must be between %d and %d", MinMemoryKb, MaxMemoryKb)
	}
	if limits.OutputKb != nil && (*limits.OutputKb < MinOutputKb || *limits.OutputKb > MaxOutputKb) {
		add("limits.output_kb", "must be between %d and %d", MinOutputKb, MaxOutputKb)
	}

	if len(request.Tests) < MinTests || len(request.Tests) > MaxTests {
		add("tests", "must contain between %d and %d tests", MinTests, MaxTests)
	}
	seen := map[string]bool{}
	for index, test := range request.Tests {
		field := fmt.Sprintf("tests[%d]", index)
		if len(test.Id) == 0 || len(test.Id) > MaxTestIDLength || !testIDPattern.MatchString(test.Id) {
			add(field+".id", "must be 1-%d characters of A-Z a-z 0-9 _ . -", MaxTestIDLength)
		} else if seen[test.Id] {
			add(field+".id", "duplicates another test id %q", test.Id)
		}
		seen[test.Id] = true
		for name, file := range map[string]contract.TestFile{"input": test.Input, "output": test.Output} {
			if !sha256Pattern.MatchString(file.Sha256) {
				add(field+"."+name+".sha256", "must be 64 lowercase hex characters")
			}
			if file.SizeBytes < 0 || file.SizeBytes > s.config.MaxTestFileBytes {
				add(field+"."+name+".size_bytes", "must be between 0 and %d", s.config.MaxTestFileBytes)
			}
		}
	}

	if message := s.checkCallbackURL(request.CallbackUrl); message != "" {
		add("callback_url", "%s", message)
	}
	return details
}

func (s *service) checkCallbackURL(raw string) string {
	if raw == "" {
		return "is required"
	}
	if len(raw) > MaxCallbackBytes {
		return fmt.Sprintf("must be at most %d bytes", MaxCallbackBytes)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "must be an http(s) URL without credentials"
	}
	for _, prefix := range s.config.AllowedCallbackPrefixes {
		if strings.HasPrefix(raw, prefix) {
			return ""
		}
	}
	return "is not in Judge's callback allow-list"
}
