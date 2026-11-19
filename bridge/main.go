package main

import (
	"flag"
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

func main() {
	os.Exit(run())
}

func run() int {
	dataDir := flag.String("data-dir", "", "directory where the bridge persists its session")
	cacheDir := flag.String("cache-dir", "", "directory for downloaded media, thumbnails and avatars (defaults to --data-dir)")
	demo := flag.Bool("demo", false, "run with a fake offline backend")
	logLevel := flag.String("log-level", "info", "log level (trace, debug, info, warn, error)")
	flag.Parse()

	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	console := zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true, TimeFormat: time.RFC3339}
	log := zerolog.New(newSanitizingWriter(console, recentErrors)).
		Level(level).With().Timestamp().Logger()
	zerolog.DefaultContextLogger = &log
	zlog.Logger = log
	stdlog.SetFlags(0)
	stdlog.SetOutput(log.With().Str("component", "stdlog").Logger())

	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "missive-bridge: --data-dir is required")
		return 2
	}
	absDir, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Err(err).Msg("Invalid data dir")
		return 2
	}
	if err = os.MkdirAll(absDir, 0o700); err != nil {
		log.Err(err).Str("data_dir", absDir).Msg("Failed to create data dir")
		return 1
	}
	restrictDir(absDir)
	absCache := absDir
	if *cacheDir != "" {
		if absCache, err = filepath.Abs(*cacheDir); err != nil {
			log.Err(err).Msg("Invalid cache dir")
			return 2
		}
		if err = os.MkdirAll(absCache, 0o700); err != nil {
			log.Err(err).Str("cache_dir", absCache).Msg("Failed to create cache dir")
			return 1
		}
		restrictDir(absCache)
		migrateCacheDirs(absDir, absCache, log.With().Str("component", "cache").Logger())
	}
	startCacheEviction(absCache, log.With().Str("component", "cache").Logger())

	out := NewOutput(os.Stdout, log)
	var backend Backend
	if *demo {
		backend = NewDemoBackend(absDir, absCache, out, log.With().Str("component", "demo").Logger())
	} else {
		backend, err = NewMetaBackend(absDir, absCache, out, log.With().Str("component", "meta").Logger())
		if err != nil {
			log.Err(err).Msg("Failed to initialize backend")
			return 1
		}
	}
	log.Info().Str("data_dir", absDir).Str("cache_dir", absCache).Bool("demo", *demo).Msg("Bridge started")

	dispatcher := NewDispatcher(backend, out, log.With().Str("component", "dispatcher").Logger())
	dispatcher.Run(os.Stdin)
	log.Info().Msg("Stdin closed, shutting down")
	dispatcher.Shutdown(10 * time.Second)
	log.Info().Msg("Bridge stopped")
	return 0
}
