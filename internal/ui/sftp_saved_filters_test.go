package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/model"
)

type savedSFTPFilterCatalog struct {
	Catalog
	filters   []model.SFTPFilter
	listErr   error
	saveErr   error
	deleteErr error
}

func (c *savedSFTPFilterCatalog) ListSFTPFilters(context.Context) ([]model.SFTPFilter, error) {
	return append([]model.SFTPFilter(nil), c.filters...), c.listErr
}

func (c *savedSFTPFilterCatalog) SaveSFTPFilter(_ context.Context, filter model.SFTPFilter) (model.SFTPFilter, error) {
	if c.saveErr != nil {
		return model.SFTPFilter{}, c.saveErr
	}
	if filter.Id == "" {
		filter.Id = "filter-" + filter.Name
	}
	c.filters = append(c.filters, filter)
	return filter, nil
}

func (c *savedSFTPFilterCatalog) DeleteSFTPFilter(_ context.Context, id string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	for index := range c.filters {
		if c.filters[index].Id == id {
			c.filters = append(c.filters[:index], c.filters[index+1:]...)
			return nil
		}
	}
	return errors.New("filter not found")
}

func TestSavedSFTPFilterAppliesToOnlyThePaneItWasOpenedFor(t *testing.T) {
	catalog := &savedSFTPFilterCatalog{filters: []model.SFTPFilter{
		{Id: "z", Name: "Text", Expression: "*.txt"},
		{Id: "a", Name: "Go", Expression: "*.go"},
	}}
	state := modelState{catalog: catalog, width: 90, height: 18, sftp: &sftpBrowserState{
		standalone: true, activeSide: sftpLocalSide,
		localEntries: []sftpclient.Entry{{Name: "main.go"}, {Name: "readme.txt"}},
		entries:      []sftpclient.Entry{{Name: "server.go"}, {Name: "config.yml"}},
	}}
	updated, command := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "F", Code: 'F'}))
	state = unwrapModel(t, updated)
	if command == nil || state.sftpFilters == nil || !state.sftpFilters.loading {
		t.Fatal("F did not open the saved-filter manager and start loading")
	}
	loaded, ok := command().(sftpFiltersLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T, want sftpFiltersLoadedMsg", command())
	}
	state.handleSFTPFiltersLoaded(loaded)
	if state.sftpFilters.filters[0].Name != "Go" {
		t.Fatalf("saved filters are not sorted by name: %#v", state.sftpFilters.filters)
	}
	updated, _ = state.updateSFTPFiltersKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.sftpFilters != nil {
		t.Fatal("applying a saved filter did not close the manager")
	}
	if got := sftpPaneEntries(state.sftp, sftpLocalSide); len(got) != 1 || got[0].Name != "main.go" {
		t.Fatalf("saved filter was not applied to the selected local pane: %#v", got)
	}
	if got := sftpPaneEntries(state.sftp, sftpRemoteSide); len(got) != 2 {
		t.Fatalf("applying a saved local filter changed the remote pane: %#v", got)
	}
}

func TestSavedSFTPFilterCanBeDeletedAfterConfirmation(t *testing.T) {
	catalog := &savedSFTPFilterCatalog{filters: []model.SFTPFilter{{Id: "go", Name: "Go", Expression: "*.go"}}}
	state := modelState{catalog: catalog, sftp: &sftpBrowserState{standalone: true}}
	state.openSFTPFilterManager()
	state.handleSFTPFiltersLoaded(sftpFiltersLoadedMsg{filters: catalog.filters})
	updated, _ := state.updateSFTPFiltersKey(tea.KeyPressMsg(tea.Key{Text: "d", Code: 'd'}))
	state = unwrapModel(t, updated)
	if !state.sftpFilters.confirmingDelete {
		t.Fatal("d did not request delete confirmation")
	}
	updated, command := state.updateSFTPFiltersKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	state = unwrapModel(t, updated)
	if command == nil || !state.sftpFilters.busy {
		t.Fatal("confirming deletion did not start the async delete")
	}
	message, ok := command().(sftpFilterDeletedMsg)
	if !ok {
		t.Fatalf("delete command returned %T, want sftpFilterDeletedMsg", command())
	}
	state.handleSFTPFilterDeleted(message)
	if len(catalog.filters) != 0 || len(state.sftpFilters.filters) != 0 || state.sftpFilters.confirmingDelete {
		t.Fatalf("deleted filter remains in store or manager: store=%#v manager=%#v", catalog.filters, state.sftpFilters)
	}
}

func TestFilterPromptSavesNamedFilterWithCtrlS(t *testing.T) {
	catalog := &savedSFTPFilterCatalog{}
	state := modelState{catalog: catalog, sftp: &sftpBrowserState{standalone: true, activeSide: sftpRemoteSide}}
	updated, _ := state.updateSFTPKey(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	state = unwrapModel(t, updated)
	state.sftp.prompt.value = "*.go, !*_test.go"
	updated, _ = state.updateSFTPPrompt(tea.Key{Text: "s", Code: 's', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if state.sftp.prompt == nil || state.sftp.prompt.action != sftpPromptSaveFilterName || state.sftp.prompt.filterExpression != "*.go, !*_test.go" {
		t.Fatal("Ctrl+S did not open the filter-name prompt with the current expression")
	}
	state.sftp.prompt.value = "Go source"
	updated, command := state.updateSFTPPrompt(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	if command == nil || state.sftp.prompt != nil {
		t.Fatal("confirming a name did not start saving and close the prompt")
	}
	message, ok := command().(sftpFilterSavedMsg)
	if !ok {
		t.Fatalf("save command returned %T, want sftpFilterSavedMsg", command())
	}
	state.handleSFTPFilterSaved(message)
	if len(catalog.filters) != 1 || catalog.filters[0].Name != "Go source" || catalog.filters[0].Expression != "*.go, !*_test.go" {
		t.Fatalf("saved filters = %#v", catalog.filters)
	}
	if state.sftp.statusErr || !strings.Contains(state.sftp.status, "Go source") {
		t.Fatalf("successful save status = %q, error=%v", state.sftp.status, state.sftp.statusErr)
	}
}

func TestSavedSFTPFilterLoadFailureIsVisible(t *testing.T) {
	catalog := &savedSFTPFilterCatalog{listErr: errors.New("database unavailable")}
	state := modelState{catalog: catalog, sftp: &sftpBrowserState{standalone: true}}
	command := state.openSFTPFilterManager()
	message := command().(sftpFiltersLoadedMsg)
	state.handleSFTPFiltersLoaded(message)
	if state.sftpFilters.loading || !strings.Contains(state.sftpFilters.errorText, "database unavailable") {
		t.Fatalf("load failure was not surfaced: %#v", state.sftpFilters)
	}
}
