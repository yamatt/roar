# roar

**roar** (RAR on a Read) is a FUSE filesystem that takes a directory of directories containing RAR archives and presents the files within those archives as if there were no RAR files.

## Features

- Present RAR archive contents as a transparent virtual filesystem
- Support for split RAR archives (.r00, .r01, .r02, etc.)
- Support for RAR5 format
- Read-only access to archive contents
- Efficient file caching for repeated reads

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

## Usage

```bash
roar [options] <source_directory> <mount_point>
```

### Options

- `-f`: Run in foreground (do not daemonize)
- `-d`: Enable debug logging
- `-v`, `--version`: Show version and exit

### Example

Suppose you have a directory structure like this:

```
/data/archives/
├── movie1/
│   ├── movie.rar
│   ├── movie.r00
│   └── movie.r01
└── movie2/
    └── video.rar
```

Mount it with roar:

```bash
mkdir /mnt/movies
roar /data/archives /mnt/movies
```

Now you can access the contents directly:

```
/mnt/movies/
├── movie1/
│   └── movie.mkv
└── movie2/
    └── video.mp4
```

To unmount:

```bash
fusermount -u /mnt/movies
# or press Ctrl+C if running in foreground
```

## Requirements

- Linux with FUSE support
- Go 1.21 or later (for building)

## How It Works

roar uses the FUSE (Filesystem in Userspace) interface to present a virtual filesystem. When you mount a source directory:

1. roar scans all subdirectories for RAR archives
2. It parses the archive structure to build a virtual directory tree
3. File reads are serviced by extracting the requested portion from the archive

The filesystem is read-only to protect your archive data.

## License

MIT
