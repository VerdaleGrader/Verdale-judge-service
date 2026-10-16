package configs

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/viper"

	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
)

// EnvPrefix is prepended to every environment override, e.g.
// server.port -> VERDALE_JUDGE_SERVER_PORT.
const EnvPrefix = "VERDALE_JUDGE"

type Configuration struct {
	Environment string          `mapstructure:"environment"`
	Server      ServerConfig    `mapstructure:"server"`
	Log         LogConfig       `mapstructure:"log"`
	Redis       RedisConfig     `mapstructure:"redis"`
	Auth        AuthConfig      `mapstructure:"auth"`
	Callback    CallbackConfig  `mapstructure:"callback"`
	Queue       QueueConfig     `mapstructure:"queue"`
	Worker      WorkerConfig    `mapstructure:"worker"`
	Sandbox     SandboxConfig   `mapstructure:"sandbox"`
	Languages   LanguagesConfig `mapstructure:"languages"`
	Storage     StorageConfig   `mapstructure:"storage"`
	Cache       CacheConfig     `mapstructure:"cache"`
}

type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	// MaxBodyBytes caps a job request: 64 KiB of source plus up to 500 tests.
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
}

type LogConfig struct {
	Level string `mapstructure:"level"`
	// Format is "json" (production) or "console" (human-readable, local).
	Format string `mapstructure:"format"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type AuthConfig struct {
	// InboundKeys is the core-to-judge keyring, "kid:secret,kid2:secret2".
	InboundKeys               string `mapstructure:"inbound_keys"`
	TimestampToleranceSeconds int    `mapstructure:"timestamp_tolerance_seconds"`
}

type CallbackConfig struct {
	// KeyID and Secret sign result callbacks (judge-to-core keyring).
	KeyID  string `mapstructure:"key_id"`
	Secret string `mapstructure:"secret"`
	// AllowedURLPrefixes limits where results may be sent, e.g.
	// "https://core.internal/internal/judge/". A job whose callback_url does
	// not start with one of them is rejected at enqueue time.
	AllowedURLPrefixes []string `mapstructure:"allowed_url_prefixes"`
	TimeoutSeconds     int      `mapstructure:"timeout_seconds"`
	InitialBackoffMs   int      `mapstructure:"initial_backoff_ms"`
	MaxBackoffSeconds  int      `mapstructure:"max_backoff_seconds"`
	// RetryWindowMinutes is how long an undelivered result keeps being
	// retried before it is moved to the dead outbox.
	RetryWindowMinutes  int `mapstructure:"retry_window_minutes"`
	PollIntervalMs      int `mapstructure:"poll_interval_ms"`
	DispatchConcurrency int `mapstructure:"dispatch_concurrency"`
}

type QueueConfig struct {
	Stream           string `mapstructure:"stream"`
	Group            string `mapstructure:"group"`
	DeadLetterStream string `mapstructure:"dead_letter_stream"`
	// ConsumerName must be unique per running Judge process. Empty means
	// "<hostname>-<pid>".
	ConsumerName string `mapstructure:"consumer_name"`
	// ReclaimIdleSeconds: a job whose worker has not sent a heartbeat for
	// this long is considered abandoned and is handed to another worker.
	ReclaimIdleSeconds     int `mapstructure:"reclaim_idle_seconds"`
	ReclaimIntervalSeconds int `mapstructure:"reclaim_interval_seconds"`
	// MaxDeliveries: after this many deliveries a job goes to the dead
	// letter stream and Core receives an ISE result.
	MaxDeliveries          int `mapstructure:"max_deliveries"`
	MetricsIntervalSeconds int `mapstructure:"metrics_interval_seconds"`
	// JobTTLHours is how long job state is kept for idempotency and GET.
	JobTTLHours int `mapstructure:"job_ttl_hours"`
}

type WorkerConfig struct {
	// Concurrency is the number of jobs judged at once, one isolate box each.
	// 0 means "match the machine": the size of sandbox.cpuset when it is set,
	// otherwise the number of CPUs minus one (a core stays free for Judge,
	// Redis and the OS).
	Concurrency            int `mapstructure:"concurrency"`
	ShutdownTimeoutSeconds int `mapstructure:"shutdown_timeout_seconds"`
}

type SandboxConfig struct {
	// Driver is "isolate". There is deliberately no unsandboxed driver.
	Driver      string `mapstructure:"driver"`
	IsolatePath string `mapstructure:"isolate_path"`
	// CgroupRoot is checked at startup for cgroup v2 (cgroup.controllers).
	CgroupRoot string `mapstructure:"cgroup_root"`
	// BoxIDBase is the first isolate box id; Judge uses
	// [BoxIDBase, BoxIDBase + worker.concurrency).
	BoxIDBase int `mapstructure:"box_id_base"`
	// Cpuset lists the cores sandboxes may use, in the kernel's format
	// ("2-7" or "1,3,5"). Each box is locked to one of them so parallel runs
	// never share a core: isolate does it from its own configuration
	// (boxN.cpus lines, written by deploy/install.sh) and Judge checks at
	// start that it did. Empty turns pinning off (development).
	Cpuset string `mapstructure:"cpuset"`
	// WorkDir holds per-job host files (compiled binaries, meta files).
	WorkDir string `mapstructure:"work_dir"`
	// Path is the PATH given to sandboxed processes.
	Path    string        `mapstructure:"path"`
	Compile CompileLimits `mapstructure:"compile"`
	// ExtraTimeMs lets a program run slightly past its limit so the real
	// time is reported; WallTimeFactor guards against sleeping programs.
	ExtraTimeMs    int `mapstructure:"extra_time_ms"`
	WallTimeFactor int `mapstructure:"wall_time_factor"`
	OpenFiles      int `mapstructure:"open_files"`
}

type CompileLimits struct {
	TimeMs            int `mapstructure:"time_ms"`
	WallTimeMs        int `mapstructure:"wall_time_ms"`
	MemoryKb          int `mapstructure:"memory_kb"`
	Processes         int `mapstructure:"processes"`
	OutputKb          int `mapstructure:"output_kb"`
	MessageLimitBytes int `mapstructure:"message_limit_bytes"`
}

type LanguagesConfig struct {
	C      LanguageConfig `mapstructure:"c"`
	Cpp    LanguageConfig `mapstructure:"cpp"`
	Python LanguageConfig `mapstructure:"python"`
}

type LanguageConfig struct {
	// Binary is the compiler (C, C++) or interpreter (Python).
	Binary string `mapstructure:"binary"`
	// Flags are passed to the compiler; for Python, to the interpreter.
	Flags []string `mapstructure:"flags"`
	// ExpectedVersion pins the toolchain: Judge refuses to start when the
	// installed version does not match exactly.
	ExpectedVersion string `mapstructure:"expected_version"`
}

type StorageConfig struct {
	// Driver is "s3" (Cloudflare R2 in production) or "local" (development).
	Driver    string   `mapstructure:"driver"`
	KeyPrefix string   `mapstructure:"key_prefix"`
	LocalDir  string   `mapstructure:"local_dir"`
	S3        S3Config `mapstructure:"s3"`
}

type S3Config struct {
	Endpoint        string `mapstructure:"endpoint"`
	Region          string `mapstructure:"region"`
	Bucket          string `mapstructure:"bucket"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key"`
}

type CacheConfig struct {
	Dir          string `mapstructure:"dir"`
	MaxBytes     int64  `mapstructure:"max_bytes"`
	MaxFileBytes int64  `mapstructure:"max_file_bytes"`
	// FetchAttempts and FetchBackoffMs control retries when storage is down.
	FetchAttempts  int `mapstructure:"fetch_attempts"`
	FetchBackoffMs int `mapstructure:"fetch_backoff_ms"`
}

// Load reads the JSON file at path (defaults, no secrets) and applies
// VERDALE_JUDGE_* environment overrides on top.
func Load(path string) (*Configuration, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("json")
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	// AutomaticEnv only overrides keys viper already knows; these are secret
	// or deployment-specific and are usually absent from the JSON file.
	for _, key := range []string{
		"redis.password", "auth.inbound_keys", "callback.key_id", "callback.secret",
		"storage.s3.access_key_id", "storage.s3.secret_access_key", "storage.s3.endpoint",
		"storage.s3.bucket", "queue.consumer_name",
	} {
		_ = v.BindEnv(key)
	}
	if raw := os.Getenv(EnvPrefix + "_CALLBACK_ALLOWED_URL_PREFIXES"); raw != "" {
		v.Set("callback.allowed_url_prefixes", splitList(raw))
	}

	cfg := &Configuration{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.resolveWorkerConcurrency(runtime.NumCPU())
	if cfg.Queue.ConsumerName == "" {
		hostname, _ := os.Hostname()
		cfg.Queue.ConsumerName = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// CPUs is the parsed sandbox.cpuset (nil when pinning is off).
func (c *Configuration) CPUs() ([]int, error) { return sandbox.ParseCPUList(c.Sandbox.Cpuset) }

// resolveWorkerConcurrency turns worker.concurrency 0 ("auto") into a number.
func (c *Configuration) resolveWorkerConcurrency(numCPU int) {
	if c.Worker.Concurrency != 0 {
		return
	}
	if cores, err := c.CPUs(); err == nil && len(cores) > 0 {
		c.Worker.Concurrency = len(cores)
		return
	}
	c.Worker.Concurrency = max(1, numCPU-1)
}

// Validate collects every problem at once so a misconfigured deploy fails
// with the full list instead of one error per restart.
func (c *Configuration) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		add("server.port must be 1-65535")
	}
	if c.Server.MaxBodyBytes < 1<<16 {
		add("server.max_body_bytes must be at least 65536")
	}
	if c.Redis.Addr == "" {
		add("redis.addr is required")
	}
	if c.Environment != "local" && c.Redis.Password == "" {
		add("redis.password is required outside local (VERDALE_JUDGE_REDIS_PASSWORD)")
	}
	if _, err := ParseKeyring(c.Auth.InboundKeys); err != nil {
		add("auth.inbound_keys (VERDALE_JUDGE_AUTH_INBOUND_KEYS): %v", err)
	}
	if c.Auth.TimestampToleranceSeconds <= 0 {
		add("auth.timestamp_tolerance_seconds must be positive")
	}
	if c.Callback.KeyID == "" || len(c.Callback.Secret) < 32 {
		add("callback.key_id and callback.secret (at least 32 bytes) are required (VERDALE_JUDGE_CALLBACK_KEY_ID, VERDALE_JUDGE_CALLBACK_SECRET)")
	}
	if len(c.Callback.AllowedURLPrefixes) == 0 {
		add("callback.allowed_url_prefixes must list at least one prefix")
	}
	for _, prefix := range c.Callback.AllowedURLPrefixes {
		if parsed, err := url.Parse(prefix); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			add("callback.allowed_url_prefixes entry %q is not an http(s) URL", prefix)
		}
	}
	if c.Callback.TimeoutSeconds <= 0 || c.Callback.InitialBackoffMs <= 0 || c.Callback.MaxBackoffSeconds <= 0 ||
		c.Callback.RetryWindowMinutes <= 0 || c.Callback.PollIntervalMs <= 0 || c.Callback.DispatchConcurrency <= 0 {
		add("callback timeouts, backoff, retry window, poll interval and concurrency must be positive")
	}
	if c.Queue.Stream == "" || c.Queue.Group == "" || c.Queue.DeadLetterStream == "" {
		add("queue.stream, queue.group and queue.dead_letter_stream are required")
	}
	if c.Queue.ReclaimIdleSeconds < 5 || c.Queue.ReclaimIntervalSeconds <= 0 || c.Queue.MaxDeliveries < 1 ||
		c.Queue.MetricsIntervalSeconds <= 0 || c.Queue.JobTTLHours <= 0 {
		add("queue.reclaim_idle_seconds must be >= 5 and the other queue intervals, max_deliveries and job_ttl_hours positive")
	}
	if c.Worker.Concurrency < 1 || c.Worker.ShutdownTimeoutSeconds < 1 {
		add("worker.concurrency and worker.shutdown_timeout_seconds must be positive")
	}
	if cores, err := c.CPUs(); err != nil {
		add("sandbox.cpuset: %v", err)
	} else if len(cores) > 0 && c.Worker.Concurrency > len(cores) {
		add("worker.concurrency (%d) is more than the %d cores in sandbox.cpuset: two boxes would share a core and measured time would differ", c.Worker.Concurrency, len(cores))
	}
	if c.Sandbox.Driver != "isolate" {
		add("sandbox.driver must be \"isolate\"")
	}
	if c.Sandbox.IsolatePath == "" || c.Sandbox.CgroupRoot == "" || c.Sandbox.WorkDir == "" || c.Sandbox.Path == "" {
		add("sandbox.isolate_path, sandbox.cgroup_root, sandbox.work_dir and sandbox.path are required")
	}
	if c.Sandbox.BoxIDBase < 0 || c.Sandbox.WallTimeFactor < 1 || c.Sandbox.ExtraTimeMs < 0 || c.Sandbox.OpenFiles < 16 {
		add("sandbox.box_id_base >= 0, wall_time_factor >= 1, extra_time_ms >= 0 and open_files >= 16 are required")
	}
	compile := c.Sandbox.Compile
	if compile.TimeMs <= 0 || compile.WallTimeMs <= 0 || compile.MemoryKb <= 0 || compile.Processes <= 0 ||
		compile.OutputKb <= 0 || compile.MessageLimitBytes <= 0 {
		add("every sandbox.compile limit must be positive")
	}
	for name, language := range map[string]LanguageConfig{"c": c.Languages.C, "cpp": c.Languages.Cpp, "python": c.Languages.Python} {
		if language.Binary == "" || language.ExpectedVersion == "" {
			add("languages.%s.binary and languages.%s.expected_version are required", name, name)
		}
	}
	switch c.Storage.Driver {
	case "local":
		if c.Storage.LocalDir == "" {
			add("storage.local_dir is required for the local driver")
		}
		if c.Environment == "production" {
			add("storage.driver \"local\" is not allowed in production")
		}
	case "s3":
		s3 := c.Storage.S3
		if s3.Endpoint == "" || s3.Bucket == "" || s3.AccessKeyID == "" || s3.SecretAccessKey == "" {
			add("storage.s3 endpoint, bucket, access_key_id and secret_access_key are required for the s3 driver")
		}
	default:
		add("storage.driver must be \"s3\" or \"local\"")
	}
	if c.Cache.Dir == "" || c.Cache.MaxBytes <= 0 || c.Cache.MaxFileBytes <= 0 || c.Cache.MaxFileBytes > c.Cache.MaxBytes {
		add("cache.dir is required and 0 < cache.max_file_bytes <= cache.max_bytes")
	}
	if c.Cache.FetchAttempts < 1 || c.Cache.FetchBackoffMs <= 0 {
		add("cache.fetch_attempts and cache.fetch_backoff_ms must be positive")
	}

	if len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}

// ParseKeyring parses "kid:secret,kid2:secret2" into a map. Every secret
// must be at least 32 bytes.
func ParseKeyring(raw string) (map[string][]byte, error) {
	keyring := map[string][]byte{}
	for _, entry := range splitList(raw) {
		keyID, secret, found := strings.Cut(entry, ":")
		if !found || keyID == "" {
			return nil, fmt.Errorf("entry %q is not kid:secret", maskEntry(entry))
		}
		if len(secret) < 32 {
			return nil, fmt.Errorf("secret for key %q is shorter than 32 bytes", keyID)
		}
		keyring[keyID] = []byte(secret)
	}
	if len(keyring) == 0 {
		return nil, errors.New("at least one key is required")
	}
	return keyring, nil
}

func splitList(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func maskEntry(entry string) string {
	if keyID, _, found := strings.Cut(entry, ":"); found {
		return keyID + ":***"
	}
	if len(entry) > 4 {
		return entry[:4] + "***"
	}
	return "***"
}
