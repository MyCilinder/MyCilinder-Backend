// Command catalog runs the MyCilinder Catalog service (Architecture §4.2): feeds, search,
// video detail, the creator studio, playback access, and operator moderation of videos.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/redisclient"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cache"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cfsign"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/config"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/events"
	httpapi "github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/http"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/jobs"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/moderation"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/playback"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/storage"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/studio"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

func main() {
	// `catalog healthcheck` probes /healthz on the local port and exits 0 or 1. The
	// runtime image has no shell or curl, so Docker and ECS container health checks run
	// this.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("catalog stopped", "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8001"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load(libconfig.FromEnv())
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger.With("service", "catalog"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// PostgreSQL through RDS Proxy (or the Compose Postgres locally). Writes and
	// read-after-write checks use the writer; feeds, search, and detail use the reader.
	writer, err := db.NewPool(ctx, db.Options{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer writer.Close()

	checks := []health.Check{{Name: "database", Fn: db.Ping(writer)}}
	reader := writer
	if cfg.DatabaseReaderURL != cfg.DatabaseURL {
		reader, err = db.NewPool(ctx, db.Options{URL: cfg.DatabaseReaderURL})
		if err != nil {
			return err
		}
		defer reader.Close()
		checks = append(checks, health.Check{Name: "database_reader", Fn: db.Ping(reader)})
	}

	// Redis holds the feed and detail cache only; reads fall back to Postgres, so an
	// outage is logged, not fatal, and it is not part of /readyz.
	rdb := redisclient.New(redisclient.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, TLS: cfg.RedisTLS})
	defer rdb.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	if err := redisclient.Ping(rdb)(pingCtx); err != nil {
		slog.Warn("redis unreachable at startup; reads will go to Postgres", "err", err)
	}
	cancelPing()

	// Cognito JWKS cache for token validation (SEC-01).
	verifier, err := jwtauth.NewCognitoVerifier(ctx, jwtauth.CognitoConfig{
		Region: cfg.CognitoRegion, UserPoolID: cfg.CognitoUserPoolID, ClientID: cfg.CognitoClientID,
	})
	if err != nil {
		return err
	}
	defer func() { _ = verifier.Close(context.Background()) }()

	// Repositories and services. Reads go to the reader and fall back to the writer.
	// Feeds, search, and READY video detail are cached in Redis (cache-aside); every
	// status change or edit drops the video:{id} entry after commit.
	videoRepo := store.NewVideos(writer, reader)
	redisCache := cache.New(cache.NewRedisStore(rdb), slog.Default())
	cachedVideos := cache.NewVideos(redisCache, videoRepo)
	machine := lifecycle.NewMachine(writer, cachedVideos)

	videos := video.NewService(cachedVideos)
	browseSvc := browse.NewService(videoRepo, cache.NewPages(redisCache))
	studioSvc := studio.NewService(writer, videoRepo, machine)
	moderationSvc := moderation.NewService(machine, store.NewAudit("catalog"))

	// Background work: the status consumer and the jobs. They stop with ctx (SIGTERM),
	// and shutdown waits for them after the HTTP server has drained.
	var background sync.WaitGroup
	defer background.Wait()

	// AWS clients (SQS, S3) use the default credential chain: AWS_PROFILE locally, the
	// task role on ECS. Loaded only when something needs them.
	var awsCfg *awsLoaded
	if cfg.ConsumerEnabled || cfg.JobsEnabled {
		awsCfg, err = loadAWS(ctx, cfg.AWSRegion)
		if err != nil {
			return err
		}
	}
	if cfg.ConsumerEnabled {
		consumer := events.NewConsumer(sqs.NewFromConfig(awsCfg.cfg), cfg.VideoStatusQueueURL, machine, slog.Default())
		background.Add(1)
		go func() { defer background.Done(); consumer.Run(ctx) }()
	}

	if cfg.JobsEnabled {
		runner := jobs.NewRunner(writer, slog.Default(),
			jobs.Expiry(cfg.JobInterval, cfg.PendingUploadExpiry, machine),
			jobs.Sweep(cfg.JobInterval, cfg.ProcessingTimeout, machine),
			jobs.Cleanup(cfg.CleanupInterval, cfg.OutputRetention,
				storage.NewCleaner(s3.NewFromConfig(awsCfg.cfg), cfg.HLSOutputBucket), slog.Default()),
		)
		background.Add(1)
		go func() { defer background.Done(); runner.Run(ctx) }()
	}

	// Signed playback cookies. Without CLOUDFRONT_* the endpoint returns 503.
	var signer playback.Signer
	if cfg.PlaybackConfigured() {
		key, err := cfsign.LoadKey(cfg.CloudFrontPrivateKeyFile)
		if err != nil {
			return err
		}
		signer = cfsign.New(cfg.CloudFrontDomain, cfg.CloudFrontKeyPairID, key)
	}
	playbackSvc := playback.NewService(cachedVideos, signer)
	slog.Info("starting",
		"consumerEnabled", cfg.ConsumerEnabled,
		"jobsEnabled", cfg.JobsEnabled,
		"playbackConfigured", cfg.PlaybackConfigured(),
		"separateReader", cfg.DatabaseReaderURL != cfg.DatabaseURL,
	)

	handler := httpapi.NewRouter(httpapi.Options{
		OriginVerifyEnabled: cfg.OriginVerifyEnabled,
		OriginVerifySecret:  cfg.OriginVerifySecret,
		ReadinessChecks:     checks,
		Verifier:            verifier,
		OperatorsGroup:      cfg.OperatorsGroup,
		Videos:              videos,
		Playback:            playbackSvc,
		Browse:              browseSvc,
		Studio:              studioSvc,
		Moderation:          moderationSvc,
		MediaDomain:         cfg.CloudFrontDomain,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	slog.Info("stopped")
	return nil
}

type awsLoaded struct{ cfg aws.Config }

func loadAWS(ctx context.Context, region string) (*awsLoaded, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := awsconfig.LoadDefaultConfig(lctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &awsLoaded{cfg: c}, nil
}
