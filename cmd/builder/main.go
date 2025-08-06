package main

import (
	"context"
	"fmt"
	"internal/adapters"
	"internal/app"
	"internal/config"
	"os"
	"os/signal"

	//"pkg/interfaces"
	"pkg/logger"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const (
	appName    = "Deploy Model"
	appVersion = "1.0.0"
)

func main() {

	//lunch zap as a logger
	log, err := logger.NewZapLogger(logger.ProdctionConfig())
	if err != nil {
		fmt.Println("FATAL:faild to start loggre ")
		//if logger  has a problem app does  not lunch
		os.Exit(1)
	}
	//anonymouse fucn to  sync log
	defer func() {
		if err := log.Sync(); err != nil {
			fmt.Println("warning faid to sync logs ", err.Error())
		}
	}()
	//print app details
	log.Info("Starting App",
		zap.String("name", appName),
		zap.String("version", appVersion),
	)

	//load configs
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("Failed to load Configuration ", zap.Error(err))
	}

	//saving context before shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setupShutdownHandler(cancel, log)

	//init the adapotrs
	adapters, err := setupAdapters(cfg, log)
	if err != nil {
		log.Fatal("Failed to initialize adapters", zap.Error(err))
	}

	//init the  build service
	if err := runBuildService(ctx, cfg, adapters, log); err != nil {
		log.Error("Build process failed", zap.Error(err))
		os.Exit(1)
	}
	log.Info("Application completed successfully")

}

func setupShutdownHandler(cancel context.CancelFunc, log logger.Logger) {
	//send  shutdown signal
	shutdownCh := make(chan os.Signal, 1)
	//call the pre  sign  for shutdown
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)
	//call  the  shutdown signal and log it
	go func() {
		sig := <-shutdownCh
		log.Info("Received shutdown signal", zap.String("signal", sig.String()))
		cancel()

		// when deploy take too much time shut down  the app with the force
		time.AfterFunc(15*time.Second, func() {
			log.Warn("Forcing shutdown after timeout")
			os.Exit(1)
		})
	}()
}

// create  adpator for each Module
func setupAdapters(cfg *config.Config, log logger.Logger) (*adapters.Adapters, error) {
	minioAdapter, err := adapters.NewMinioAdapter(cfg.Minio, log)
	if err != nil {
		return nil, err
	}

	dockerAdapter, err := adapters.NewDockerAdapter(cfg.Docker, log)
	if err != nil {
		return nil, err
	}

	archiveAdapter := adapters.NewArchiveAdapter(log)

	return &adapters.Adapters{
		Storage: minioAdapter,
		Builder: dockerAdapter,
		Archive: archiveAdapter,
	}, nil
}

func runBuildService(
	ctx context.Context,
	cfg *config.Config,
	adapters *adapters.Adapters,
	log logger.Logger,
) error {
	buildService := app.NewBuildService(
		adapters.Storage,
		adapters.Builder,
		adapters.Archive,
		cfg,
		log,
	)

	return buildService.Run(ctx)
}
