package sftpclient

import (
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type metadataFileInfo struct {
	name string
	size int64
	mode os.FileMode
	stat *sftp.FileStat
}

func (info metadataFileInfo) Name() string       { return info.name }
func (info metadataFileInfo) Size() int64        { return info.size }
func (info metadataFileInfo) Mode() os.FileMode  { return info.mode }
func (info metadataFileInfo) ModTime() time.Time { return time.Unix(10, 0) }
func (info metadataFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info metadataFileInfo) Sys() any           { return info.stat }

func TestEntryFromFileInfoPreservesZeroOwnerAndUnknownOwnership(t *testing.T) {
	root := entryFromFileInfo(metadataFileInfo{
		name: "root-owned", mode: 0o600,
		stat: &sftp.FileStat{UID: 0, GID: 0, UIDGIDKnown: true},
	})
	if !root.OwnershipKnown || root.UID != 0 || root.GID != 0 {
		t.Fatalf("root ownership entry = %#v; expected known zero IDs", root)
	}

	unknown := entryFromFileInfo(metadataFileInfo{
		name: "unknown-owner", mode: 0o600,
		stat: &sftp.FileStat{UID: 1000, GID: 1000},
	})
	if unknown.OwnershipKnown {
		t.Fatalf("entry with absent UID/GID flags was reported as known: %#v", unknown)
	}
}
