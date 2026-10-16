package configs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaultsWithSecretsFromEnvironment(t *testing.T) {
	t.Setenv("VERDALE_JUDGE_AUTH_INBOUND_KEYS", "core-1:0123456789abcdef0123456789abcdef")
	t.Setenv("VERDALE_JUDGE_CALLBACK_KEY_ID", "judge-1")
	t.Setenv("VERDALE_JUDGE_CALLBACK_SECRET", "fedcba9876543210fedcba9876543210")
	t.Setenv("VERDALE_JUDGE_SERVER_PORT", "9999")
	t.Setenv("VERDALE_JUDGE_QUEUE_CONSUMER_NAME", "judge-a")

	cfg, err := Load("../config.json")
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.Server.Port, "env overrides the file")
	assert.Equal(t, "judge-a", cfg.Queue.ConsumerName)
	assert.Equal(t, "12.2.0", cfg.Languages.Cpp.ExpectedVersion)
	assert.Equal(t, "fedcba9876543210fedcba9876543210", cfg.Callback.Secret)
}

func TestLoadRefusesIncompleteConfiguration(t *testing.T) {
	_, err := Load("../config.json")
	require.Error(t, err, "secrets are not in config.json")
	assert.Contains(t, err.Error(), "auth.inbound_keys")
	assert.Contains(t, err.Error(), "callback.key_id")

	path := filepath.Join(t.TempDir(), "broken.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"environment":"production","server":{"port":0},"storage":{"driver":"local"}}`), 0o600))
	_, err = Load(path)
	require.Error(t, err)
	for _, expected := range []string{"server.port", "redis.addr", "redis.password", "sandbox.driver", "storage.local_dir", "not allowed in production"} {
		assert.Contains(t, err.Error(), expected)
	}
}

func TestParseKeyring(t *testing.T) {
	keyring, err := ParseKeyring("old:0123456789abcdef0123456789abcdef, new:fedcba9876543210fedcba9876543210")
	require.NoError(t, err)
	assert.Len(t, keyring, 2)

	_, err = ParseKeyring("short:abc")
	assert.ErrorContains(t, err, "shorter than 32 bytes")
	_, err = ParseKeyring("no-separator")
	assert.ErrorContains(t, err, "not kid:secret")
	assert.NotContains(t, err.Error(), "separator", "secrets are masked in errors")
	_, err = ParseKeyring("")
	assert.Error(t, err)
}

func TestWorkerConcurrencyFollowsTheMachine(t *testing.T) {
	cases := map[string]struct {
		configured int
		cpuset     string
		numCPU     int
		expected   int
	}{
		"explicit number is kept":      {configured: 3, cpuset: "", numCPU: 16, expected: 3},
		"auto with a cpuset":           {configured: 0, cpuset: "2-5", numCPU: 16, expected: 4},
		"auto without a cpuset":        {configured: 0, cpuset: "", numCPU: 8, expected: 7},
		"auto on a one-core machine":   {configured: 0, cpuset: "", numCPU: 1, expected: 1},
		"auto with a scattered cpuset": {configured: 0, cpuset: "1,3,5", numCPU: 8, expected: 3},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Configuration{Worker: WorkerConfig{Concurrency: testCase.configured}, Sandbox: SandboxConfig{Cpuset: testCase.cpuset}}
			cfg.resolveWorkerConcurrency(testCase.numCPU)
			assert.Equal(t, testCase.expected, cfg.Worker.Concurrency)
		})
	}
}

func TestValidateRefusesMoreWorkersThanCores(t *testing.T) {
	cfg := &Configuration{Worker: WorkerConfig{Concurrency: 5, ShutdownTimeoutSeconds: 1}, Sandbox: SandboxConfig{Cpuset: "2-4"}}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than the 3 cores in sandbox.cpuset")

	cfg.Sandbox.Cpuset = "x"
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox.cpuset")
}
