package repository

import (
	"path/filepath"
	"testing"
)

func TestAuthorizeFileShareUsesLiveCapabilitiesAndIdentity(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "fileshare.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"idA", "idB"} {
		_, err := db.Exec(`INSERT INTO identities
			(id, name, status, capabilities, created_by, updated_by, created_at, updated_at)
			VALUES (?, ?, 'active', '[]', 'test', 'test', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, id, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []struct { id, identity, caps string }{
		{"client", "idA", `["files.client"]`},
		{"host", "idA", `["files.host"]`},
		{"other", "idB", `["files.host"]`},
	} {
		_, err := db.Exec(`INSERT INTO devices
			(id, name, public_key_fingerprint, installation_id, approval_state,
			 requested_capabilities, approved_capabilities, identity_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'approved', ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			d.id, d.id, d.id+"-fp", d.id+"-installation", d.caps, d.caps, d.identity)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(client, host string, want bool) {
		t.Helper()
		ok, err := db.AuthorizeFileShare(client, host)
		if err != nil || ok != want {
			t.Fatalf("AuthorizeFileShare(%q,%q)=%v,%v; want %v", client, host, ok, err, want)
		}
	}
	check("client","host",true)
	check("client","other",false)
	check("host","client",false)
	check("client","client",false)
	check("missing","host",false)
	if _, err := db.Exec(`UPDATE devices SET approved_capabilities='[]' WHERE id='host'`); err != nil {t.Fatal(err)}
	check("client","host",false)
	if _, err := db.Exec(`UPDATE devices SET approved_capabilities='["files.host"]', approval_state='revoked' WHERE id='host'`); err != nil {t.Fatal(err)}
	check("client","host",false)
	if _, err := db.Exec(`UPDATE devices SET approval_state='approved' WHERE id='host'`); err != nil {t.Fatal(err)}
	if _, err := db.Exec(`UPDATE identities SET status='disabled' WHERE id='idA'`); err != nil {t.Fatal(err)}
	check("client","host",false)
}
