package web_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/migrate"
)

// The receiving endpoints answer only a request sealed with the key this
// panel issued — never one sealed with another key, one with no key, or one
// replayed — and their answers are sealed too.
func TestMigrationEndpointsAnswerOnlyTheirKey(t *testing.T) {
	p := start(t)

	key, err := migrate.NewKey()
	if err != nil {
		t.Fatal(err)
	}

	// Nothing is accepted before a key is issued.
	client, _ := migrate.NewClient(p.server.URL, key)
	var hello migrate.Hello
	if err := client.Call(migrate.PathHello, struct{}{}, &hello); err == nil {
		t.Fatal("a panel with no migration key answered")
	}

	p.db.SetSetting("migration_key_id", key.ID)
	p.db.SetSecretSetting("migration_key_secret", string(key.Secret))

	if err := client.Call(migrate.PathHello, struct{}{}, &hello); err != nil {
		t.Fatalf("hello with the right key: %v", err)
	}
	if hello.Panel == "" || hello.Version == "" || hello.Services == nil {
		t.Errorf("hello said too little: %+v", hello)
	}

	other, _ := migrate.NewKey()
	other.ID = key.ID // the right id, the wrong secret
	impostor, _ := migrate.NewClient(p.server.URL, other)
	if err := impostor.Call(migrate.PathHello, struct{}{}, &hello); err == nil {
		t.Error("a request sealed with another secret was answered")
	}

	// A sealed request sent twice is refused the second time.
	sealed, _ := key.Seal("request "+migrate.PathHello, []byte(`{}`))
	send := func() int {
		req, _ := http.NewRequest(http.MethodPost, p.server.URL+migrate.PathHello, bytes.NewReader(sealed))
		req.Header.Set(migrate.KeyHeader, key.ID)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := send(); code != http.StatusOK {
		t.Fatalf("first send = %d", code)
	}
	if code := send(); code != http.StatusUnauthorized {
		t.Errorf("a replayed request answered %d", code)
	}

	// An unknown transfer is an error said inside the seal, not a crash.
	err = client.Call(migrate.PathStatus, migrate.Transfer{Transfer: strings.Repeat("a", 32)}, nil)
	if err == nil || !strings.Contains(err.Error(), "no such transfer") {
		t.Errorf("status of an unknown transfer: %v", err)
	}

	// A transfer id that is not one is refused the same way.
	err = client.Call(migrate.PathStatus, migrate.Transfer{Transfer: "../../etc"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no such transfer") {
		t.Errorf("status of a path as a transfer: %v", err)
	}
	if id := migrate.NewTransferID(); !migrate.ValidTransferID(id) {
		t.Errorf("a new transfer id %q is not a valid one", id)
	}

	// Revoking the key stops everything.
	p.db.ForgetSetting("migration_key_id")
	if err := client.Call(migrate.PathHello, struct{}{}, &hello); err == nil {
		t.Error("a revoked key was still answered")
	}
}
