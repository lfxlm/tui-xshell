# Local SFTP dependency patch

This is a local fork of `github.com/pkg/sftp` v1.13.9, retained under the
upstream BSD-2-Clause license in `LICENSE`.

The only behavior change is to expose `FileStat.UIDGIDKnown`, populated from the
SFTP attribute flags during decoding. This lets the file browser distinguish a
real root owner (`UID=0`, `GID=0`) from a server that omitted ownership fields.
The workbench needs this distinction to avoid inventing file metadata.

When upgrading the upstream dependency, reapply this small patch to
`FileStat` and `unmarshalFileStat`, then keep
`attrs_presence_test.go` passing.
