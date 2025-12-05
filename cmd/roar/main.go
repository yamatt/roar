// Command roar mounts a directory containing RAR archives as a FUSE filesystem,
// presenting the contents of the archives as if they were regular files.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yamatt/roar/internal/rarfs"
)

var version = "dev"

// checkFuseAvailability checks if FUSE libraries are installed and available.
// It verifies both the fusermount command and /dev/fuse device.
func checkFuseAvailability(logger *slog.Logger) error {
	// Check for fusermount command
	if _, err := exec.LookPath("fusermount"); err != nil {
		return fmt.Errorf("fusermount command not found. Please install FUSE libraries:\n" +
			"  Debian/Ubuntu: sudo apt-get install fuse libfuse2\n" +
			"  Fedora/RHEL:   sudo dnf install fuse fuse-libs\n" +
			"  Arch Linux:    sudo pacman -S fuse2")
	}

	// Check for /dev/fuse device
	if _, err := os.Stat("/dev/fuse"); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("/dev/fuse not found. FUSE kernel module may not be loaded.\n" +
				"Try loading it with: sudo modprobe fuse")
		}
		return fmt.Errorf("error accessing /dev/fuse: %w", err)
	}

	logger.Debug("FUSE libraries available")
	return nil
}

func main() {
	var showVersion bool
	var allowOther bool

	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "v", false, "Show version and exit (shorthand)")
	flag.BoolVar(&allowOther, "allow-other", false, "Allow other users to access the mounted filesystem (requires user_allow_other in /etc/fuse.conf)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] <source_directory> <mount_point>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "roar presents RAR archives in a directory as a virtual filesystem.\n")
		fmt.Fprintf(os.Stderr, "The source directory should contain subdirectories with RAR files.\n")
		fmt.Fprintf(os.Stderr, "Supports split RAR files (.r00, .r01, etc.) and RAR5 format.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
		fmt.Fprintf(os.Stderr, "  ROAR_LOG_LEVEL\n")
		fmt.Fprintf(os.Stderr, "    \tSet log level (debug, info, warn, error). Default: info\n")
	}
	flag.Parse()

	if showVersion {
		fmt.Printf("roar version %s\n", version)
		os.Exit(0)
	}

	// Set up structured logging
	// Log level can be set via ROAR_LOG_LEVEL environment variable
	// Valid values: debug, info, warn, error
	logLevel := slog.LevelInfo
	if envLevel := os.Getenv("ROAR_LOG_LEVEL"); envLevel != "" {
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

	// Check if FUSE libraries are installed
	if err := checkFuseAvailability(logger); err != nil {
		logger.Error("FUSE not available", "error", err)
		os.Exit(1)
	}

	logger.Info("mounting filesystem", "source", sourceDir, "mountPoint", mountPoint)

	server, rfs, err := rarfs.Mount(sourceDir, mountPoint, allowOther)
	if err != nil {
		logger.Error("failed to mount filesystem", "error", err)
		os.Exit(1)
	}

	logger.Info("filesystem mounted successfully, press Ctrl+C to unmount")

	// Handle signals for clean shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info("received signal, unmounting...")
		err := server.Unmount()
		if err != nil {
			logger.Error("error unmounting", "error", err)
		}
	}()
	server.Wait()

	// Clean up the watcher
	if err := rfs.Close(); err != nil {
		logger.Error("error closing filesystem", "error", err)
	}

	logger.Info("filesystem unmounted")
}
