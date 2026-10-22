package ui

import (
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestInheritedGroupStartDirsUsesNearestValuePerSide(t *testing.T) {
	state := modelState{groups: []model.Group{
		{Id: "root", Name: "Root", LocalStartDir: "~/Downloads", RemoteStartDir: "/srv"},
		{Id: "child", Name: "Child", ParentId: "root", RemoteStartDir: "/srv/dev"},
		{Id: "leaf", Name: "Leaf", ParentId: "child"},
	}}

	localStartDir, remoteStartDir := state.inheritedGroupStartDirs("leaf")
	if localStartDir != "~/Downloads" || remoteStartDir != "/srv/dev" {
		t.Fatalf("inherited group paths = %q / %q, want nearest local/remote values", localStartDir, remoteStartDir)
	}

	host := state.hostWithGroupStartDirs(model.Host{GroupId: "leaf", LocalStartDir: "/host/override"})
	if host.LocalStartDir != "/host/override" || host.RemoteStartDir != "/srv/dev" {
		t.Fatalf("host start paths = %q / %q, want host local override and inherited remote path", host.LocalStartDir, host.RemoteStartDir)
	}
}

func TestGroupEditFormCopiesAndEditsStartDirectories(t *testing.T) {
	form := newGroupEditForm(model.Group{
		Id: "group-1", Name: "Development", LocalStartDir: "~/work", RemoteStartDir: "/srv/work",
	})
	if form.fieldCount() != 5 || form.valueAt(3) != "~/work" || form.valueAt(4) != "/srv/work" {
		t.Fatalf("group edit fields = count %d, local %q, remote %q", form.fieldCount(), form.valueAt(3), form.valueAt(4))
	}
	form.field = 3
	form.setCurrentText("~/projects")
	form.field = 4
	form.setCurrentText("/opt/releases")
	if form.localStartDir != "~/projects" || form.remoteStartDir != "/opt/releases" {
		t.Fatalf("edited group start paths = %q / %q", form.localStartDir, form.remoteStartDir)
	}
}
