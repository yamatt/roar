# roar 🦁

A FUSE filesystem that takes directories containing RAR archives and presents the files within those archives as if there were no RAR files.

## Features

- Present RAR archive contents as a transparent virtual filesystem
- Support for split RAR archives:
  - New style: `.part1.rar`, `.part2.rar`, etc.
  - Old style: `.rar`, `.r00`, `.r01`, etc.
- Support for RAR5 format
- Read-only access to archive contents
- Pass-through for non-RAR files (files that aren't RAR archives are accessible in the mounted filesystem)
- Efficient file caching for repeated reads

## Requirements

### Runtime Requirements

- Linux with FUSE support
- FUSE libraries installed on your system

#### Installing FUSE Libraries

**Debian/Ubuntu:**
```bash
sudo apt-get install fuse libfuse2
```

**Fedora/RHEL/CentOS:**
```bash
sudo dnf install fuse fuse-libs
```

**Arch Linux:**
```bash
sudo pacman -S fuse2
```

### Build Requirements

- Go 1.25 or later
- FUSE development headers (for building)

**Debian/Ubuntu:**
```bash
sudo apt-get install libfuse-dev
```

**Fedora/RHEL/CentOS:**
```bash
sudo dnf install fuse-devel
```

**Arch Linux:**
```bash
sudo pacman -S fuse2
```

## Installation

### From Source

```bash
go install github.com/yamatt/roar/cmd/roar@latest
```

### Building Locally

```bash
git clone https://github.com/yamatt/roar.git
cd roar
go build -o roar ./cmd/roar
```

### Using Docker

Pre-built Docker images are available on GitHub Container Registry:

```bash
docker pull ghcr.io/yamatt/roar:latest
```

## Usage

```bash
roar [options] <source_directory> <mount_point>
```

### Options

- `-v`, `--version`: Show version and exit

### Environment Variables

- `ROAR_LOG_LEVEL`: Set the log level (debug, info, warn, error). Default: info

### Example

Suppose you have a directory structure like this:

```
/data/archives/
├── archive1/
│   ├── compressed-1.rar
│   ├── compressed-1.r00
│   └── compressed-1.r01
├── archive2/
│   ├── compressed-2.part1.rar
│   ├── compressed-2.part2.rar
│   └── compressed-2.part3.rar
└── archive3/
    ├── compressed-3.rar
    └── info.txt
```

Mount it with roar:

```bash
mkdir /mnt/unarchived
roar /data/archives /mnt/unarchived
```

Now you can access the contents directly:

```
/mnt/unarchived/
├── archive1/
│   └── home-movie.mkv
├── archive2/
│   └── video.mp4
└── archive3/
    ├── my-project.avi
    └── info.txt    # Non-RAR files are passed through
```

To unmount:

```bash
fusermount -u /mnt/movies
# or press Ctrl+C if running in foreground
```

## How It Works

roar uses the FUSE (Filesystem in Userspace) interface to present a virtual filesystem. When you mount a source directory:

1. roar discovers only the immediate children of the source directory (lazy loading)
2. Subdirectories are discovered lazily when you navigate into them
3. RAR archives are scanned only when their containing directory is accessed
4. File reads are serviced by extracting the requested portion from the archive
5. Non-RAR files in the source directories are passed through and accessible directly

This lazy loading approach means roar starts quickly even with large directory structures containing many subdirectories.

The filesystem is read-only to protect your archive data.

## License

MIT
