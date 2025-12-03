// Command roar mounts a directory containing RAR archives as a FUSE filesystem,
// presenting the contents of the archives as if they were regular files.
package main

import (
	"flag"
	"fmt"
	"log"
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
	)

	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "v", false, "Show version and exit (shorthand)")
	flag.BoolVar(&foreground, "f", false, "Run in foreground (do not daemonize)")
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
		log.Fatalf("Error accessing source directory: %v", err)
	}
	if !sourceInfo.IsDir() {
		log.Fatalf("Source path is not a directory: %s", sourceDir)
	}

	// Convert to absolute paths
	sourceDir, err = filepath.Abs(sourceDir)
	if err != nil {
		log.Fatalf("Error resolving source directory path: %v", err)
	}

	mountPoint, err = filepath.Abs(mountPoint)
	if err != nil {
		log.Fatalf("Error resolving mount point path: %v", err)
	}

	// Ensure mount point exists and is a directory
	mountInfo, err := os.Stat(mountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("Mount point does not exist: %s", mountPoint)
		}
		log.Fatalf("Error accessing mount point: %v", err)
	}
	if !mountInfo.IsDir() {
		log.Fatalf("Mount point is not a directory: %s", mountPoint)
	}

	log.Printf("Mounting %s at %s", sourceDir, mountPoint)

	server, err := rarfs.Mount(sourceDir, mountPoint)
	if err != nil {
		log.Fatalf("Failed to mount filesystem: %v", err)
	}

	log.Printf("Filesystem mounted successfully. Press Ctrl+C to unmount.")

	// Handle signals for clean shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	if foreground {
		// Wait for signal
		<-sigChan
		log.Printf("Received signal, unmounting...")
		err = server.Unmount()
		if err != nil {
			log.Printf("Error unmounting: %v", err)
		}
	} else {
		// Run until unmounted
		go func() {
			<-sigChan
			log.Printf("Received signal, unmounting...")
			err := server.Unmount()
			if err != nil {
				log.Printf("Error unmounting: %v", err)
			}
		}()
		server.Wait()
	}

	log.Printf("Filesystem unmounted.")
}
