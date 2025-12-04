// Command roar mounts a directory containing RAR archives as a FUSE filesystem,
// presenting the contents of the archives as if they were regular files.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yamatt/roar/internal/rarfs"
)

var version = "dev"

func main() {
	var (
		showVersion bool
		foreground  bool
		debug       bool
	)

	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "v", false, "Show version and exit (shorthand)")
	flag.BoolVar(&foreground, "f", false, "Run in foreground (do not daemonize)")
	flag.BoolVar(&debug, "d", false, "Enable debug logging")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <source_directory> <mount_point>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "roar presents RAR archives in a directory as a virtual filesystem.\n")
		fmt.Fprintf(os.Stderr, "The source directory should contain subdirectories with RAR files.\n")
		fmt.Fprintf(os.Stderr, "Supports split RAR files (.r00, .r01, etc.) and RAR5 format.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Printf("roar version %s\n", version)
		os.Exit(0)
	}

	// Set up structured logging
	// Log level can be set via -d flag or ROAR_LOG_LEVEL environment variable
	// Valid values for ROAR_LOG_LEVEL: debug, info, warn, error
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	} else if envLevel := os.Getenv("ROAR_LOG_LEVEL"); envLevel != "" {
		switch envLevel {
		case "debug":
			logLevel = slog.LevelDebug
		case "info":
			logLevel = slog.LevelInfo
		case "warn":
			logLevel = slog.LevelWarn
		case "error":
			logLevel = slog.LevelError
		}
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	rarfs.SetLogger(logger)

	args := flag.Args()
	if len(args) != 2 {
		flag.Usage()
		os.Exit(1)
	}

	sourceDir := args[0]
	mountPoint := args[1]

	// Validate source directory
	sourceInfo, err := os.Stat(sourceDir)
	if err != nil {
		logger.Error("error accessing source directory", "error", err)
		os.Exit(1)
	}
	if !sourceInfo.IsDir() {
		logger.Error("source path is not a directory", "path", sourceDir)
		os.Exit(1)
	}

	// Convert to absolute paths
	sourceDir, err = filepath.Abs(sourceDir)
	if err != nil {
		logger.Error("error resolving source directory path", "error", err)
		os.Exit(1)
	}

	mountPoint, err = filepath.Abs(mountPoint)
	if err != nil {
		logger.Error("error resolving mount point path", "error", err)
		os.Exit(1)
	}

	// Ensure mount point exists and is a directory
	mountInfo, err := os.Stat(mountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Error("mount point does not exist", "path", mountPoint)
			os.Exit(1)
		}
		logger.Error("error accessing mount point", "error", err)
		os.Exit(1)
	}
	if !mountInfo.IsDir() {
		logger.Error("mount point is not a directory", "path", mountPoint)
		os.Exit(1)
	}

	logger.Info("mounting filesystem", "source", sourceDir, "mountPoint", mountPoint)

	server, err := rarfs.Mount(sourceDir, mountPoint)
	if err != nil {
		logger.Error("failed to mount filesystem", "error", err)
		os.Exit(1)
	}

	logger.Info("filesystem mounted successfully, press Ctrl+C to unmount")

	// Handle signals for clean shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if foreground {
		// Wait for signal
		<-sigChan
		logger.Info("received signal, unmounting...")
		err = server.Unmount()
		if err != nil {
			logger.Error("error unmounting", "error", err)
		}
	} else {
		// Run until unmounted
		go func() {
			<-sigChan
			logger.Info("received signal, unmounting...")
			err := server.Unmount()
			if err != nil {
				logger.Error("error unmounting", "error", err)
			}
		}()
		server.Wait()
	}

	logger.Info("filesystem unmounted")
}
