// Package remotefs defines the file operations shared by remote protocols.
package remotefs

import (
	"context"
	"errors"
	"os"
	"time"
)

// Entry contains protocol-neutral metadata used by the file manager.
// Protocols that cannot report an attribute leave it at its zero value.
type Entry struct {
	Name      string
	Size      int64
	Mode      os.FileMode
	ModeKnown bool
	UID       uint32
	GID       uint32
	// OwnershipKnown distinguishes a real root owner (UID/GID 0) from
	// protocols or servers that do not provide owner attributes.
	OwnershipKnown bool
	ModTime        time.Time
	IsDir          bool
	IsSymlink      bool
}

// SearchMatch identifies a matching entry and its path below a search root.
type SearchMatch struct {
	Path  string
	Entry Entry
}

// FileVersion detects edits made by another process while a file is open.
type FileVersion struct {
	Size    int64
	ModTime time.Time
	Mode    os.FileMode
	Digest  [32]byte
}

var ErrRemoteFileChanged = errors.New("远程文件已发生变化")

const MaxEditableFileSize = 1 << 20

// Client is the remote file surface required by the shared Xftp-style UI.
// Implementations must document which metadata operations their protocol
// cannot provide; unsupported operations should return an explicit error.
type Client interface {
	Close() error
	WorkingDirectory() (string, error)
	RealPath(remotePath string) (string, error)
	List(remotePath string) ([]Entry, error)
	Search(ctx context.Context, root, query string, resultLimit int) ([]SearchMatch, bool, error)
	Exists(remotePath string) (bool, error)
	MakeDirectory(remotePath string) error
	Rename(oldPath, newPath string) error
	SetPermissions(remotePath string, mode os.FileMode) error
	Remove(remotePath string, directory bool) error
	RemoveTree(remotePath string) error
	RemoveTreeContext(ctx context.Context, remotePath string) error
	Upload(ctx context.Context, localPath, remotePath string, overwrite bool, progress func(int64, int64)) error
	Download(ctx context.Context, remotePath, localPath string, overwrite bool, progress func(int64, int64)) error
	CopyRemoteFileTo(ctx context.Context, destination Client, sourcePath, destinationPath string, overwrite bool, progress func(int64, int64)) error
	CopyRemoteDirectoryTo(ctx context.Context, destination Client, sourceRoot, destinationRoot string, overwrite bool, progress func(int64, int64)) error
	ReadTextFile(remotePath string, maxSize int64) ([]byte, FileVersion, error)
	SaveTextFile(ctx context.Context, remotePath string, content []byte, expected FileVersion, progress func(int64, int64)) (FileVersion, error)
	UploadDirectory(ctx context.Context, localRoot, remoteRoot string, overwrite bool, progress func(int64, int64)) error
	DownloadDirectory(ctx context.Context, remoteRoot, localRoot string, overwrite bool, progress func(int64, int64)) error
}
