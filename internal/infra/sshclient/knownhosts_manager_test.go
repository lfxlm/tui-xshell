package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"tui-ssh-workbench/internal/model"
)

func TestListAndRemoveKnownHostEntryPreservesOtherLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	firstKey := testHostKey(t)
	secondKey := testHostKey(t)
	contents := "# managed by test\n" +
		knownhosts.Line([]string{"first.example:22"}, firstKey) + "\n" +
		knownhosts.Line([]string{"[second.example]:2222"}, secondKey) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := listKnownHostEntries(path)
	if err != nil {
		t.Fatalf("list known hosts: %v", err)
	}
	if len(entries) != 2 || entries[0].LineNumber != 2 || entries[1].LineNumber != 3 {
		t.Fatalf("parsed known host entries = %#v", entries)
	}
	if entries[1].Hosts[0] != "[second.example]:2222" || entries[1].KeyType != secondKey.Type() || entries[1].Fingerprint != ssh.FingerprintSHA256(secondKey) {
		t.Fatalf("second known host metadata = %#v", entries[1])
	}
	if err := removeKnownHostEntry(path, entries[1]); err != nil {
		t.Fatalf("remove second host key: %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), "second.example") || !strings.Contains(string(updated), "first.example") || !strings.HasPrefix(string(updated), "# managed by test\n") {
		t.Fatalf("known_hosts after removal = %q", updated)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts permissions = %v, err=%v; want 0600", info, err)
	}
}

func TestRemoveKnownHostEntryRejectsStaleSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testHostKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"host.example"}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := listKnownHostEntries(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list entry = %#v, err=%v", entries, err)
	}
	entry := entries[0]
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"host.example"}, testHostKey(t))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeKnownHostEntry(path, entry); err != ErrKnownHostEntryChanged {
		t.Fatalf("stale remove error = %v, want %v", err, ErrKnownHostEntryChanged)
	}
}

func TestListKnownHostEntriesReportsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("host.example not-a-key value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listKnownHostEntries(path); err == nil || !strings.Contains(err.Error(), "第 1 行无效") {
		t.Fatalf("malformed known host line error = %v", err)
	}
}

func TestPlanAndApplyKnownHostsImportDeduplicatesAndPreservesMarkers(t *testing.T) {
	path := isolatedKnownHostsPath(t)
	existingKey := testHostKey(t)
	newKey := testHostKey(t)
	caKey := testHostKey(t)
	initial := "# keep this comment\n" + knownhosts.Line([]string{"existing.example"}, existingKey)
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "known_hosts")
	caLine := strings.Fields(knownhosts.Line([]string{"*.example.org"}, caKey))
	contents := "# exported from OpenSSH\n" +
		knownhosts.Line([]string{"existing.example"}, existingKey) + "\n" +
		knownhosts.Line([]string{"new.example"}, newKey) + "\n" +
		"@cert-authority " + strings.Join(caLine, " ") + "\n"
	if err := os.WriteFile(source, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanKnownHostsImport(source)
	if err != nil {
		t.Fatalf("plan import: %v", err)
	}
	if plan.TotalRecords != 3 || plan.NewRecords != 2 {
		t.Fatalf("import plan counts = total %d, new %d; want 3, 2", plan.TotalRecords, plan.NewRecords)
	}
	before, err := os.ReadFile(path)
	if err != nil || string(before) != initial {
		t.Fatalf("planning modified the trust store: %q, err=%v", before, err)
	}
	added, err := ApplyKnownHostsImport(plan)
	if err != nil || added != 2 {
		t.Fatalf("apply import = %d, %v; want 2 records", added, err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), initial+"\n") || strings.Count(string(updated), "existing.example") != 1 || !strings.Contains(string(updated), "@cert-authority *.example.org") {
		t.Fatalf("merged trust store did not preserve/deduplicate records: %q", updated)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts permissions = %v, err=%v; want 0600", info, err)
	}
}

func TestKnownHostsImportRejectsMalformedAndUnsupportedRecordsWithoutChanges(t *testing.T) {
	path := isolatedKnownHostsPath(t)
	initial := "# leave existing data intact\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{
		"host.example not-a-key value\n",
		"@unknown-marker host.example ssh-ed25519 AAAA\n",
	} {
		source := filepath.Join(t.TempDir(), "known_hosts")
		if err := os.WriteFile(source, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PlanKnownHostsImport(source); err == nil {
			t.Errorf("planning malformed import %q succeeded", contents)
		}
	}
	updated, err := os.ReadFile(path)
	if err != nil || string(updated) != initial {
		t.Fatalf("failed import changed trust store: %q, err=%v", updated, err)
	}
}

func TestApplyKnownHostsImportRejectsChangedSourceOrDestination(t *testing.T) {
	path := isolatedKnownHostsPath(t)
	key := testHostKey(t)
	source := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(source, []byte(knownhosts.Line([]string{"host.example"}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanKnownHostsImport(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# changed after preview\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyKnownHostsImport(plan); err != ErrKnownHostsImportChanged {
		t.Fatalf("changed destination error = %v, want %v", err, ErrKnownHostsImportChanged)
	}
	plan, err = PlanKnownHostsImport(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("# source changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyKnownHostsImport(plan); err != ErrKnownHostsImportChanged {
		t.Fatalf("changed source error = %v, want %v", err, ErrKnownHostsImportChanged)
	}
}

func TestExportKnownHostsFileIsPrivateAndNeverOverwrites(t *testing.T) {
	path := isolatedKnownHostsPath(t)
	key := testHostKey(t)
	contents := "# app trust store\n" + knownhosts.Line([]string{"host.example"}, key) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "known_hosts.export")
	count, err := ExportKnownHostsFile(destination)
	if err != nil || count != 1 {
		t.Fatalf("export = %d, %v; want one record", count, err)
	}
	exported, err := os.ReadFile(destination)
	if err != nil || string(exported) != contents {
		t.Fatalf("exported known_hosts = %q, err=%v; want exact source", exported, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export permissions = %v, err=%v; want 0600", info, err)
	}
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportKnownHostsFile(destination); err == nil {
		t.Fatal("export overwrote an existing destination")
	}
	preserved, err := os.ReadFile(destination)
	if err != nil || string(preserved) != "keep" {
		t.Fatalf("existing export destination changed: %q, err=%v", preserved, err)
	}
}

func TestReplaceKnownHostKeyReplacesLiteralHostAndPreservesOtherAliases(t *testing.T) {
	path := isolatedKnownHostsPath(t)
	oldKey, newKey := testHostKey(t), testHostKey(t)
	contents := "# retain comment\n" + knownhosts.Line([]string{"[192.0.2.10]:2222", "alias.example"}, oldKey) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ListKnownHostEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("list existing host keys = %#v, err=%v", entries, err)
	}
	reference := KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: entries[0].Fingerprint}
	host := model.Host{Kind: model.HostKindSSH, IP: "192.0.2.10", Port: 2222}
	if err := ReplaceKnownHostKey(host, newKey, []KnownHostKeyReference{reference}); err != nil {
		t.Fatalf("replace host key: %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), "# retain comment\n") || !strings.Contains(string(updated), "alias.example") || strings.Contains(string(updated), "[192.0.2.10]:2222 alias.example") {
		t.Fatalf("replacement did not preserve the unrelated alias and comment: %q", updated)
	}
	callback, err := knownhosts.New(path)
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 2222}
	if err := callback("[192.0.2.10]:2222", remote, newKey); err != nil {
		t.Fatalf("new key was not trusted after replacement: %v", err)
	}
	if err := callback("[192.0.2.10]:2222", remote, oldKey); err == nil {
		t.Fatal("old host key remained trusted after replacement")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("replaced known_hosts permissions = %v, err=%v; want 0600", info, err)
	}
}

func TestReplaceKnownHostKeySupportsHashedHostsAndRejectsUnsafeRows(t *testing.T) {
	t.Run("hashed host", func(t *testing.T) {
		path := isolatedKnownHostsPath(t)
		oldKey, newKey := testHostKey(t), testHostKey(t)
		address := "192.0.2.11"
		contents := knownhosts.Line([]string{knownhosts.HashHostname(address)}, oldKey) + "\n"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err := ListKnownHostEntries()
		if err != nil || len(entries) != 1 {
			t.Fatalf("list hashed host entry = %#v, err=%v", entries, err)
		}
		reference := KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: entries[0].Fingerprint}
		if err := ReplaceKnownHostKey(model.Host{Kind: model.HostKindSSH, IP: address, Port: 22}, newKey, []KnownHostKeyReference{reference}); err != nil {
			t.Fatalf("replace hashed host key: %v", err)
		}
		callback, err := knownhosts.New(path)
		if err != nil {
			t.Fatal(err)
		}
		remote := &net.TCPAddr{IP: net.ParseIP(address), Port: 22}
		if err := callback(address+":22", remote, newKey); err != nil {
			t.Fatalf("new key was not trusted after hashed-host replacement: %v", err)
		}
	})

	t.Run("wildcard row", func(t *testing.T) {
		path := isolatedKnownHostsPath(t)
		oldKey := testHostKey(t)
		if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"*.example.com"}, oldKey)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err := ListKnownHostEntries()
		if err != nil || len(entries) != 1 {
			t.Fatalf("list wildcard entry = %#v, err=%v", entries, err)
		}
		reference := KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: entries[0].Fingerprint}
		err = ReplaceKnownHostKey(model.Host{Kind: model.HostKindSSH, IP: "build.example.com", Port: 22}, testHostKey(t), []KnownHostKeyReference{reference})
		if err == nil || !strings.Contains(err.Error(), "通配符") {
			t.Fatalf("wildcard replacement error = %v", err)
		}
	})

	for _, marker := range []string{"@cert-authority", "@revoked"} {
		t.Run(marker+" row", func(t *testing.T) {
			path := isolatedKnownHostsPath(t)
			oldKey := testHostKey(t)
			fields := strings.Fields(knownhosts.Line([]string{"marked.example"}, oldKey))
			contents := marker + " " + strings.Join(fields, " ") + "\n"
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			entries, err := ListKnownHostEntries()
			if err != nil || len(entries) != 1 {
				t.Fatalf("list marked entry = %#v, err=%v", entries, err)
			}
			reference := KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: entries[0].Fingerprint}
			err = ReplaceKnownHostKey(model.Host{Kind: model.HostKindSSH, IP: "marked.example", Port: 22}, testHostKey(t), []KnownHostKeyReference{reference})
			if err != ErrKnownHostKeyChanged {
				t.Fatalf("marked-row replacement error = %v, want %v", err, ErrKnownHostKeyChanged)
			}
			updated, readErr := os.ReadFile(path)
			if readErr != nil || string(updated) != contents {
				t.Fatalf("marked-row replacement changed trust store: %q, err=%v", updated, readErr)
			}
		})
	}

	t.Run("stale reference", func(t *testing.T) {
		path := isolatedKnownHostsPath(t)
		oldKey := testHostKey(t)
		if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"host.example"}, oldKey)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err := ListKnownHostEntries()
		if err != nil || len(entries) != 1 {
			t.Fatalf("list stale reference entry = %#v, err=%v", entries, err)
		}
		reference := KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: entries[0].Fingerprint}
		changed := knownhosts.Line([]string{"host.example"}, testHostKey(t)) + "\n"
		if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}
		err = ReplaceKnownHostKey(model.Host{Kind: model.HostKindSSH, IP: "host.example", Port: 22}, testHostKey(t), []KnownHostKeyReference{reference})
		if err != ErrKnownHostKeyChanged {
			t.Fatalf("stale replacement error = %v, want %v", err, ErrKnownHostKeyChanged)
		}
		updated, readErr := os.ReadFile(path)
		if readErr != nil || string(updated) != changed {
			t.Fatalf("stale replacement changed trust store: %q, err=%v", updated, readErr)
		}
	})
}

func isolatedKnownHostsPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path, err := ensureKnownHostsFile()
	if err != nil {
		t.Fatalf("prepare isolated known_hosts: %v", err)
	}
	return path
}

func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}
