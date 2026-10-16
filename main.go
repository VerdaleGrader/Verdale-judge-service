package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/kanonkc/verdale-judge-service/configs"
	controllers_system "github.com/kanonkc/verdale-judge-service/controllers/system"
	"github.com/kanonkc/verdale-judge-service/pkg/hmacauth"
	"github.com/kanonkc/verdale-judge-service/pkg/logger"
	"github.com/kanonkc/verdale-judge-service/pkg/metrics"
	"github.com/kanonkc/verdale-judge-service/pkg/queue"
	"github.com/kanonkc/verdale-judge-service/pkg/runner"
	"github.com/kanonkc/verdale-judge-service/pkg/sandbox"
	"github.com/kanonkc/verdale-judge-service/pkg/testfiles"
	"github.com/kanonkc/verdale-judge-service/repositories/jobstore"
	"github.com/kanonkc/verdale-judge-service/repositories/outbox"
	"github.com/kanonkc/verdale-judge-service/routes"
	"github.com/kanonkc/verdale-judge-service/services/callback"
	services_jobs "github.com/kanonkc/verdale-judge-service/services/jobs"
	"github.com/kanonkc/verdale-judge-service/services/pipeline"
	"github.com/kanonkc/verdale-judge-service/services/worker"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	configPath := flag.String("config", envOr("VERDALE_JUDGE_CONFIG", "config.json"), "path to the JSON config file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "verdale-judge:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := configs.Load(configPath)
	if err != nil {
		return err
	}
	log, err := logger.New(cfg.Log.Level, cfg.Log.Format, cfg.Environment, version)
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	zap.ReplaceGlobals(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- Redis (queue, job state, outbox) ---
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer redisClient.Close()
	if err := waitForRedis(ctx, redisClient, 30*time.Second); err != nil {
		return err
	}
	jobQueue := queue.New(redisClient, queue.Config{Stream: cfg.Queue.Stream, Group: cfg.Queue.Group, DeadLetterStream: cfg.Queue.DeadLetterStream})
	if err := jobQueue.EnsureGroup(ctx); err != nil {
		return err
	}

	// --- Sandbox and toolchains: refuse to start if they are not ready ---
	for _, dir := range []string{cfg.Sandbox.WorkDir, cfg.Cache.Dir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	cpus, err := cfg.CPUs()
	if err != nil {
		return err
	}
	isolate, err := sandbox.NewIsolate(sandbox.IsolateConfig{
		IsolatePath: cfg.Sandbox.IsolatePath,
		CgroupRoot:  cfg.Sandbox.CgroupRoot,
		BoxIDBase:   cfg.Sandbox.BoxIDBase,
		BoxCount:    cfg.Worker.Concurrency,
		CPUs:        cpus,
		MetaDir:     filepath.Join(cfg.Sandbox.WorkDir, "meta"),
	})
	if err != nil {
		return err
	}
	if err := isolate.CheckEnvironment(ctx); err != nil {
		return fmt.Errorf("sandbox environment check failed, refusing to start: %w", err)
	}
	languages := runner.Languages(cfg.Languages)
	if err := runner.CheckVersions(ctx, languages, sandbox.ExecRunner); err != nil {
		return fmt.Errorf("toolchain check failed, refusing to start: %w", err)
	}
	log.Info("sandbox and toolchains ready", zap.Int("boxes", cfg.Worker.Concurrency), zap.Ints("cpus", cpus))

	// --- Test files ---
	var store testfiles.ObjectStore
	if cfg.Storage.Driver == "s3" {
		store = testfiles.NewS3Store(testfiles.S3Options{
			Endpoint: cfg.Storage.S3.Endpoint, Region: cfg.Storage.S3.Region, Bucket: cfg.Storage.S3.Bucket,
			AccessKeyID: cfg.Storage.S3.AccessKeyID, SecretAccessKey: cfg.Storage.S3.SecretAccessKey,
		})
	} else {
		store = testfiles.LocalStore{Dir: cfg.Storage.LocalDir}
	}
	cache, err := testfiles.OpenDiskCache(cfg.Cache.Dir, cfg.Cache.MaxBytes)
	if err != nil {
		return err
	}
	fetcher := testfiles.NewFetcher(store, cache, testfiles.FetcherConfig{
		KeyPrefix: cfg.Storage.KeyPrefix, MaxFileBytes: cfg.Cache.MaxFileBytes,
		Attempts: cfg.Cache.FetchAttempts, Backoff: time.Duration(cfg.Cache.FetchBackoffMs) * time.Millisecond,
	})

	// --- Services ---
	appMetrics := metrics.New()
	jobs := jobstore.New(redisClient, cfg.Queue.Stream, time.Duration(cfg.Queue.JobTTLHours)*time.Hour)
	results := outbox.New(redisClient)
	compile := cfg.Sandbox.Compile
	judgePipeline := pipeline.New(isolate, fetcher, languages, pipeline.Config{
		WorkDir:        cfg.Sandbox.WorkDir,
		Path:           cfg.Sandbox.Path,
		ExtraTime:      time.Duration(cfg.Sandbox.ExtraTimeMs) * time.Millisecond,
		WallTimeFactor: cfg.Sandbox.WallTimeFactor,
		OpenFiles:      cfg.Sandbox.OpenFiles,
		Compile: pipeline.CompileLimits{
			CPUTime: time.Duration(compile.TimeMs) * time.Millisecond, WallTime: time.Duration(compile.WallTimeMs) * time.Millisecond,
			MemoryKB: compile.MemoryKb, Processes: compile.Processes, OutputKB: compile.OutputKb, MessageLimitBytes: compile.MessageLimitBytes,
		},
		JudgeVersion: version,
	})
	jobWorker := worker.New(jobQueue, jobs, results, judgePipeline, worker.Config{
		Consumer:        cfg.Queue.ConsumerName,
		Concurrency:     cfg.Worker.Concurrency,
		ReclaimIdle:     time.Duration(cfg.Queue.ReclaimIdleSeconds) * time.Second,
		ReclaimInterval: time.Duration(cfg.Queue.ReclaimIntervalSeconds) * time.Second,
		MaxDeliveries:   int64(cfg.Queue.MaxDeliveries),
		MetricsInterval: time.Duration(cfg.Queue.MetricsIntervalSeconds) * time.Second,
		ShutdownTimeout: time.Duration(cfg.Worker.ShutdownTimeoutSeconds) * time.Second,
		JudgeVersion:    version,
	}, appMetrics, log)
	dispatcher := callback.NewDispatcher(results, hmacauth.Signer{KeyID: cfg.Callback.KeyID, Secret: []byte(cfg.Callback.Secret)}, callback.Config{
		Timeout:        time.Duration(cfg.Callback.TimeoutSeconds) * time.Second,
		InitialBackoff: time.Duration(cfg.Callback.InitialBackoffMs) * time.Millisecond,
		MaxBackoff:     time.Duration(cfg.Callback.MaxBackoffSeconds) * time.Second,
		RetryWindow:    time.Duration(cfg.Callback.RetryWindowMinutes) * time.Minute,
		PollInterval:   time.Duration(cfg.Callback.PollIntervalMs) * time.Millisecond,
		Concurrency:    cfg.Callback.DispatchConcurrency,
	}, appMetrics, log)

	keyring, _ := configs.ParseKeyring(cfg.Auth.InboundKeys) // validated in Load
	router := routes.Setup(routes.Dependencies{
		Logger:       log,
		Metrics:      appMetrics,
		Verifier:     hmacauth.Verifier{Keyring: keyring, Tolerance: time.Duration(cfg.Auth.TimestampToleranceSeconds) * time.Second},
		MaxBodyBytes: cfg.Server.MaxBodyBytes,
		JobService:   services_jobs.New(jobs, services_jobs.Config{AllowedCallbackPrefixes: cfg.Callback.AllowedURLPrefixes, MaxTestFileBytes: cfg.Cache.MaxFileBytes}),
		Version:      version,
		ReadyChecks: map[string]controllers_system.Check{
			"redis":   func(ctx context.Context) bool { return redisClient.Ping(ctx).Err() == nil },
			"sandbox": func(context.Context) bool { return true }, // checked before the server starts
		},
	})
	server := &http.Server{
		Addr:              net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// --- Run ---
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); jobWorker.Run(ctx) }()
	go func() { defer background.Done(); dispatcher.Run(ctx) }()

	serverErr := make(chan error, 1)
	go func() {
		log.Info("judge listening", zap.String("addr", server.Addr), zap.String("consumer", cfg.Queue.ConsumerName))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serverErr:
		stop()
		log.Error("http server failed", zap.Error(err))
	}

	// Stop accepting jobs first, then let running jobs and deliveries finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn("http server did not shut down cleanly", zap.Error(err))
	}
	background.Wait()
	log.Info("judge stopped")
	return nil
}

func waitForRedis(ctx context.Context, client *redis.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := client.Ping(ctx).Err()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("redis is not reachable: %w", err)
		}
		time.Sleep(time.Second)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
