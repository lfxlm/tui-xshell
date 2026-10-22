package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type savedSFTPBookmarkCatalog struct {
	Catalog
	bookmarks []model.SFTPBookmark
}

func (c *savedSFTPBookmarkCatalog) ListSFTPBookmarks(_ context.Context, scopeKey string, remote bool) ([]model.SFTPBookmark, error) {
	var result []model.SFTPBookmark
	for _, bookmark := range c.bookmarks {
		if bookmark.ScopeKey == scopeKey && bookmark.Remote == remote {
			result = append(result, bookmark)
		}
	}
	return result, nil
}

func (c *savedSFTPBookmarkCatalog) SaveSFTPBookmark(_ context.Context, bookmark model.SFTPBookmark) (model.SFTPBookmark, error) {
	for _, existing := range c.bookmarks {
		if existing.ScopeKey == bookmark.ScopeKey && existing.Remote == bookmark.Remote && strings.EqualFold(existing.Name, bookmark.Name) {
			return model.SFTPBookmark{}, errors.New("duplicate bookmark")
		}
	}
	bookmark.Id = "bookmark-" + bookmark.Name
	c.bookmarks = append(c.bookmarks, bookmark)
	return bookmark, nil
}

func (c *savedSFTPBookmarkCatalog) DeleteSFTPBookmark(_ context.Context, id string) error {
	for index, bookmark := range c.bookmarks {
		if bookmark.Id == id {
			c.bookmarks = append(c.bookmarks[:index], c.bookmarks[index+1:]...)
			return nil
		}
	}
	return errors.New("bookmark not found")
}

func TestSFTPBookmarkManagerSavesAndJumpsToCurrentDirectory(t *testing.T) {
	directory := t.TempDir()
	catalog := &savedSFTPBookmarkCatalog{}
	state := modelState{
		catalog: catalog, width: 90, height: 20,
		sftp: &sftpBrowserState{standalone: true, activeSide: sftpLocalSide, localPath: directory},
	}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "B", Code: 'B'}))
	state = unwrapModel(t, updated)
	if command == nil || state.sftpBookmarks == nil || !state.sftpBookmarks.loading {
		t.Fatal("B did not open and load the directory bookmark manager")
	}
	loaded, ok := command().(sftpBookmarksLoadedMsg)
	if !ok {
		t.Fatalf("bookmark load returned %T", command())
	}
	state.handleSFTPBookmarksLoaded(loaded)

	updated, _ = state.updateSFTPBookmarksKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}))
	state = unwrapModel(t, updated)
	if !state.sftpBookmarks.nameInput {
		t.Fatal("a did not begin saving the current directory")
	}
	state.sftpBookmarks.nameValue = "Workspace"
	updated, command = state.updateSFTPBookmarksKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftpBookmarks.busy {
		t.Fatal("Enter did not save the new bookmark")
	}
	saved, ok := command().(sftpBookmarkSavedMsg)
	if !ok || saved.err != nil || saved.bookmark.Path != filepath.Clean(directory) || saved.bookmark.Remote {
		t.Fatalf("saved bookmark result = %#v", saved)
	}
	state.handleSFTPBookmarkSaved(saved)
	if len(state.sftpBookmarks.bookmarks) != 1 || state.sftpBookmarks.bookmarks[0].Name != "Workspace" {
		t.Fatalf("saved bookmark was not added to the manager: %#v", state.sftpBookmarks.bookmarks)
	}

	updated, command = state.updateSFTPBookmarksKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.sftpBookmarks != nil || state.sftp.activeSide != sftpLocalSide || command == nil {
		t.Fatal("Enter did not close the manager and navigate the bookmarked pane")
	}
}

func TestSFTPBookmarkManagerDeletesOnlyAfterConfirmation(t *testing.T) {
	catalog := &savedSFTPBookmarkCatalog{bookmarks: []model.SFTPBookmark{{
		Id: "bookmark-logs", ScopeKey: "host:ssh-1", Name: "Logs", Path: "/var/log", Remote: true,
	}}}
	state := modelState{catalog: catalog, sftp: &sftpBrowserState{
		standalone: true, host: model.Host{Id: "ssh-1", Kind: model.HostKindSSH},
		client: &sftpNavigationFakeClient{}, activeSide: sftpRemoteSide,
	}}
	command := state.openSFTPBookmarkManager()
	if command == nil {
		t.Fatal("opening the remote bookmark manager did not load bookmarks")
	}
	loaded, _ := command().(sftpBookmarksLoadedMsg)
	state.handleSFTPBookmarksLoaded(loaded)
	updated, _ := state.updateSFTPBookmarksKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	state = unwrapModel(t, updated)
	if !state.sftpBookmarks.confirmingDelete || len(catalog.bookmarks) != 1 {
		t.Fatal("d deleted the bookmark without the confirmation step")
	}
	updated, command = state.updateSFTPBookmarksKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("y did not confirm bookmark deletion")
	}
	deleted, _ := command().(sftpBookmarkDeletedMsg)
	state.handleSFTPBookmarkDeleted(deleted)
	if len(catalog.bookmarks) != 0 || len(state.sftpBookmarks.bookmarks) != 0 {
		t.Fatalf("confirmed bookmark deletion left records: catalog=%#v manager=%#v", catalog.bookmarks, state.sftpBookmarks.bookmarks)
	}
}

func TestSFTPBookmarkScopeFollowsHostAndLocalSide(t *testing.T) {
	state := &sftpBrowserState{
		standalone: true,
		host:       model.Host{Id: "right-host", Kind: model.HostKindSSH},
		peerHost:   model.Host{Id: "left-host", Kind: model.HostKindFTP},
		peerClient: &sftpNavigationFakeClient{},
	}
	if got := sftpBookmarkScopeKey(state, sftpRemoteSide); got != "host:right-host" {
		t.Fatalf("right remote bookmark scope = %q", got)
	}
	if got := sftpBookmarkScopeKey(state, sftpLocalSide); got != "host:left-host" {
		t.Fatalf("left remote bookmark scope = %q", got)
	}
	state.peerClient = nil
	if got := sftpBookmarkScopeKey(state, sftpLocalSide); got != "local" {
		t.Fatalf("local bookmark scope = %q, want local", got)
	}
	state.host = model.Host{Kind: model.HostKindFTP, IP: "files.example.test", Port: 21, User: "operator"}
	first := sftpBookmarkScopeKey(state, sftpRemoteSide)
	second := sftpBookmarkScopeKey(state, sftpRemoteSide)
	state.host.User = "another"
	if first != second || first == sftpBookmarkScopeKey(state, sftpRemoteSide) {
		t.Fatal("temporary remote bookmark scope is not stable or does not distinguish users")
	}
}
