package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloudnvr/internal/cloud"
	"cloudnvr/internal/config"
	"cloudnvr/internal/domain"
	"cloudnvr/internal/id"
	"cloudnvr/internal/media"
	"cloudnvr/internal/security"
	"cloudnvr/internal/store"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.CloudFromEnv()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	cipher, err := security.NewCipher(cfg.CameraEncryptionKey)
	if err != nil {
		logger.Error("invalid encryption configuration", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("mysql", cfg.DatabaseDSN)
	if err != nil {
		logger.Error("database setup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}

	dataStore := store.New(db)
	if err := dataStore.Migrate(ctx); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	if err := media.ValidatePublishBase(cfg.MediaInternalRTSPURL); err != nil {
		logger.Error("invalid media configuration", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           cloud.NewServer(dataStore, cipher, cfg, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	background, backgroundCancel := context.WithCancel(context.Background())
	defer backgroundCancel()
	go markOffline(background, dataStore, logger)
	relays := media.NewRelayManager(logger)
	defer relays.Stop()
	recorders := media.NewRecorderManager(logger, cfg.RecordingsPath, cfg.RecordingSegmentTime)
	defer recorders.Stop()
	go syncDirectCameras(background, dataStore, cipher, cfg, relays, logger)
	go syncCloudRecordings(background, dataStore, cfg, recorders, logger)
	go func() {
		logger.Info("cloud API listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()

	stop, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	<-stop.Done()
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdown)
}

func syncCloudRecordings(ctx context.Context, dataStore *store.Store, cfg config.Cloud, recorders *media.RecorderManager, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		cameras, err := dataStore.ListAllCameras(ctx)
		if err != nil {
			logger.Error("could not load recording policies", "error", err)
		} else {
			if deleted, storageErr := media.EnforceMinimumFreeSpace(cfg.RecordingsPath, cfg.StorageIdentityFile, cfg.MinimumFreeBytes, 2*cfg.RecordingSegmentTime); storageErr != nil {
				logger.Error("cloud recording storage below safety threshold", "error", storageErr)
				recorders.Reconcile(ctx, nil)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					continue
				}
			} else if deleted > 0 {
				logger.Warn("oldest cloud recordings removed to preserve free space", "count", deleted)
			}
			streams := make([]media.RecordingStream, 0, len(cameras))
			for _, camera := range cameras {
				cloudRecording := camera.RecordingMode == domain.ModeCloud || camera.RecordingMode == domain.ModeHybrid ||
					(camera.AccessMode == domain.AccessDirect && camera.RecordingMode == domain.ModeLocal) ||
					(camera.RecordingMode == domain.ModeManual && camera.ManualRecording)
				if !camera.Enabled || !cloudRecording {
					continue
				}
				inputURL, buildErr := media.PublishURL(cfg.MediaInternalRTSPURL, cfg.MediaPublishUser, cfg.MediaPublishPassword, camera.ID)
				if buildErr != nil {
					logger.Error("could not build recording input", "camera_id", camera.ID, "error", buildErr)
					continue
				}
				retention := camera.CloudRetentionDays
				if camera.AccessMode == domain.AccessDirect && camera.RecordingMode == domain.ModeLocal {
					retention = camera.LocalRetentionDays
				}
				streams = append(streams, media.RecordingStream{ID: camera.ID, Name: camera.Name, InputURL: inputURL, RetentionDays: retention, Enabled: true})
			}
			recorders.Reconcile(ctx, streams)
			files, scanErr := media.ScanRecordings(cfg.RecordingsPath, streams, 10*time.Second)
			if scanErr != nil {
				logger.Error("could not scan cloud recordings", "error", scanErr)
			} else {
				_ = media.PopulateChecksums(files, 4)
				inventoryToken, tokenErr := id.New()
				if tokenErr != nil {
					logger.Error("could not create cloud recording inventory", "error", tokenErr)
					continue
				}
				inventoryComplete := true
				for _, file := range files {
					recordingID, _ := id.New()
					ended := file.EndedAt
					recording := domain.Recording{ID: recordingID, CameraID: file.CameraID, Source: "cloud", StorageKey: file.StorageKey,
						StartedAt: file.StartedAt, EndedAt: &ended, SizeBytes: file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256, EventType: "continuous"}
					if err := dataStore.UpsertRecordingInventory(ctx, recording, inventoryToken); err != nil {
						logger.Error("could not index recording", "camera_id", file.CameraID, "error", err)
						inventoryComplete = false
					}
				}
				if inventoryComplete {
					cameraIDs := make([]string, 0, len(streams))
					for _, stream := range streams {
						cameraIDs = append(cameraIDs, stream.ID)
					}
					deleted, reconcileErr := dataStore.ReconcileRecordingInventory(ctx, "cloud", cameraIDs, inventoryToken)
					if reconcileErr != nil {
						logger.Error("could not reconcile cloud recordings", "error", reconcileErr)
					} else if deleted > 0 {
						logger.Info("orphaned cloud recordings removed", "count", deleted)
					}
				}
				for _, stream := range streams {
					if stream.RetentionDays > 0 {
						_ = dataStore.PruneCloudRecordings(ctx, stream.ID, time.Now().Add(-time.Duration(stream.RetentionDays)*24*time.Hour))
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncDirectCameras(ctx context.Context, dataStore *store.Store, cipher *security.Cipher, cfg config.Cloud, relays *media.RelayManager, logger *slog.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		cameras, secrets, err := dataStore.ListCamerasByAccessMode(ctx, domain.AccessDirect)
		if err != nil {
			logger.Error("could not load direct cameras", "error", err)
		} else {
			streams := make([]media.Stream, 0, len(cameras))
			for _, camera := range cameras {
				inputURL, decryptErr := cipher.Decrypt(secrets[camera.ID])
				if decryptErr != nil {
					logger.Error("could not decrypt camera URL", "camera_id", camera.ID, "error", decryptErr)
					continue
				}
				outputURL, buildErr := media.PublishURL(cfg.MediaInternalRTSPURL, cfg.MediaPublishUser, cfg.MediaPublishPassword, camera.ID)
				if buildErr != nil {
					logger.Error("could not build media URL", "camera_id", camera.ID, "error", buildErr)
					continue
				}
				streams = append(streams, media.Stream{ID: camera.ID, InputURL: inputURL, OutputURL: outputURL, Enabled: camera.Enabled})
			}
			relays.Reconcile(ctx, streams)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func markOffline(ctx context.Context, dataStore *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := dataStore.MarkStaleAgentsOffline(ctx); err != nil {
				logger.Error("could not mark stale agents offline", "error", err)
			}
		}
	}
}
