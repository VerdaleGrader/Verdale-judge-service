package jobs_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/pkg/contract"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
	"github.com/kanonkc/verdale-judge-service/routes"
	services_jobs "github.com/kanonkc/verdale-judge-service/services/jobs"
)

const (
	keyID  = "core-1"
	secret = "core-to-judge-secret-0123456789abcdef"
)

type api struct {
	handler http.Handler
	client  *redis.Client
}

func newAPI(t *testing.T) *api {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := jobstore.New(client, "judge:jobs", time.Hour)
	router := routes.Setup(routes.Dependencies{
		Logger:       zap.NewNop(),
		Metrics:      metrics.New(),
		Verifier:     hmacauth.Verifier{Keyring: map[string][]byte{keyID: []byte(secret)}, Tolerance: 300 * time.Second},
		MaxBodyBytes: 1 << 20,
		JobService:   services_jobs.New(store, services_jobs.Config{AllowedCallbackPrefixes: []string{"http://core:8080/internal/judge/"}, MaxTestFileBytes: 1 << 28}),
		Version:      "test",
	})
	return &api{handler: router, client: client}
}

func (a *api) do(t *testing.T, method, path string, body []byte, sign bool, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if sign {
		hmacauth.Signer{KeyID: keyID, Secret: []byte(secret)}.SignRequest(request, path, body)
	}
	for _, change := range mutate {
		change(request)
	}
	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, request)
	return recorder
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func validJob() map[string]any {
	return map[string]any{
		"job_id":   uuid.NewString(),
		"kind":     "submission",
		"language": "cpp",
		"source":   "#include <cstdio>\nint main(){int a,b;scanf(\"%d %d\",&a,&b);printf(\"%d\\n\",a+b);}",
		"limits":   map[string]any{"time_ms": 1000, "memory_kb": 262144},
		"tests": []map[string]any{
			{"id": "1", "input": map[string]any{"sha256": sha("1 2\n"), "size_bytes": 4}, "output": map[string]any{"sha256": sha("3\n"), "size_bytes": 2}, "sample": true},
		},
		"callback_url": "http://core:8080/internal/judge/results",
	}
}

func encode(t *testing.T, value any) []byte {
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func errorBody(t *testing.T, recorder *httptest.ResponseRecorder) contract.ErrorResponse {
	var body contract.ErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestCreateJobAndGetIt(t *testing.T) {
	api := newAPI(t)
	job := validJob()
	body := encode(t, job)

	recorder := api.do(t, http.MethodPost, "/internal/jobs", body, true)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	var accepted contract.JobAccepted
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &accepted))
	assert.Equal(t, contract.JobStatusQueued, accepted.Status)
	assert.False(t, accepted.Duplicate)
	assert.NotEmpty(t, recorder.Header().Get("X-Request-Id"))

	path := "/internal/jobs/" + job["job_id"].(string)
	recorder = api.do(t, http.MethodGet, path, nil, true)
	require.Equal(t, http.StatusOK, recorder.Code)
	var state contract.JobState
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &state))
	assert.Equal(t, contract.JobStatusQueued, state.Status)
}

func TestDuplicateJobIsIdempotent(t *testing.T) {
	api := newAPI(t)
	job := validJob()

	first := api.do(t, http.MethodPost, "/internal/jobs", encode(t, job), true)
	require.Equal(t, http.StatusAccepted, first.Code)

	// Same job, different key order and whitespace.
	pretty, err := json.MarshalIndent(job, "", "    ")
	require.NoError(t, err)
	second := api.do(t, http.MethodPost, "/internal/jobs", pretty, true)
	require.Equal(t, http.StatusAccepted, second.Code)
	var accepted contract.JobAccepted
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &accepted))
	assert.True(t, accepted.Duplicate)

	length, err := api.client.XLen(t.Context(), "judge:jobs").Result()
	require.NoError(t, err)
	assert.EqualValues(t, 1, length, "only one job enqueued")

	job["source"] = "int main(){return 1;}"
	conflict := api.do(t, http.MethodPost, "/internal/jobs", encode(t, job), true)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Equal(t, "JOB_ID_CONFLICT", errorBody(t, conflict).ErrorCode)
}

func TestSignatureIsRequired(t *testing.T) {
	api := newAPI(t)
	body := encode(t, validJob())

	cases := map[string]struct {
		sign   bool
		mutate func(*http.Request)
		code   string
	}{
		"unsigned":    {false, nil, "MISSING_SIGNATURE"},
		"unknown key": {true, func(r *http.Request) { r.Header.Set(hmacauth.HeaderKeyID, "attacker") }, "UNKNOWN_KEY_ID"},
		"old timestamp": {false, func(r *http.Request) {
			hmacauth.Signer{KeyID: keyID, Secret: []byte(secret), Now: func() time.Time { return time.Now().Add(-10 * time.Minute) }}.SignRequest(r, "/internal/jobs", body)
		}, "STALE_TIMESTAMP"},
		"wrong secret": {false, func(r *http.Request) {
			hmacauth.Signer{KeyID: keyID, Secret: []byte("not-the-real-secret-0123456789abcdef")}.SignRequest(r, "/internal/jobs", body)
		}, "INVALID_SIGNATURE"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			var mutations []func(*http.Request)
			if testCase.mutate != nil {
				mutations = append(mutations, testCase.mutate)
			}
			recorder := api.do(t, http.MethodPost, "/internal/jobs", body, testCase.sign, mutations...)
			assert.Equal(t, http.StatusUnauthorized, recorder.Code)
			assert.Equal(t, testCase.code, errorBody(t, recorder).ErrorCode)
		})
	}
	length, _ := api.client.XLen(t.Context(), "judge:jobs").Result()
	assert.EqualValues(t, 0, length, "nothing unsigned is ever enqueued")
}

func TestValidationReportsEveryField(t *testing.T) {
	api := newAPI(t)
	job := validJob()
	job["kind"] = "contest"
	job["language"] = "java"
	job["source"] = strings.Repeat("x", 65537)
	job["limits"] = map[string]any{"time_ms": 50, "memory_kb": 1, "output_kb": 0}
	job["tests"] = []map[string]any{
		{"id": "a b", "input": map[string]any{"sha256": "XYZ", "size_bytes": -1}, "output": map[string]any{"sha256": sha("x"), "size_bytes": 1}},
		{"id": "dup", "input": map[string]any{"sha256": sha("x"), "size_bytes": 1}, "output": map[string]any{"sha256": sha("x"), "size_bytes": 1}},
		{"id": "dup", "input": map[string]any{"sha256": sha("x"), "size_bytes": 1}, "output": map[string]any{"sha256": sha("x"), "size_bytes": 1}},
	}
	job["callback_url"] = "http://evil.example/steal"

	recorder := api.do(t, http.MethodPost, "/internal/jobs", encode(t, job), true)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	body := errorBody(t, recorder)
	assert.Equal(t, "VALIDATION_FAILED", body.ErrorCode)
	require.NotNil(t, body.Details)
	fields := map[string]bool{}
	for _, detail := range *body.Details {
		fields[detail.Field] = true
	}
	for _, field := range []string{
		"kind", "language", "source", "limits.time_ms", "limits.memory_kb", "limits.output_kb",
		"tests[0].id", "tests[0].input.sha256", "tests[0].input.size_bytes", "tests[2].id", "callback_url",
	} {
		assert.True(t, fields[field], "expected a detail for %s, got %v", field, fields)
	}
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	api := newAPI(t)
	job := validJob()
	job["priority"] = "high" // a future optional field
	recorder := api.do(t, http.MethodPost, "/internal/jobs", encode(t, job), true)
	assert.Equal(t, http.StatusAccepted, recorder.Code)
}

func TestOversizedBody(t *testing.T) {
	api := newAPI(t)
	body := bytes.Repeat([]byte("x"), (1<<20)+1)
	recorder := api.do(t, http.MethodPost, "/internal/jobs", body, true)
	assert.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	assert.Equal(t, "BODY_TOO_LARGE", errorBody(t, recorder).ErrorCode)
}

func TestErrorShapeForUnknownJobAndRoute(t *testing.T) {
	api := newAPI(t)
	recorder := api.do(t, http.MethodGet, "/internal/jobs/"+uuid.NewString(), nil, true)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	body := errorBody(t, recorder)
	assert.Equal(t, "JOB_NOT_FOUND", body.ErrorCode)
	assert.Equal(t, 404, body.Status)
	assert.NotEmpty(t, body.RequestId)

	recorder = api.do(t, http.MethodGet, "/nope", nil, false)
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, "NOT_FOUND", errorBody(t, recorder).ErrorCode)
}
