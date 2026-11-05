package web_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A read-only account sees pages and changes nothing — not even another
// account — while its own profile stays its own.
func TestReadOnlyAccountsChangeNothing(t *testing.T) {
	p := start(t)

	viewer, err := p.db.CreateUser(context.Background(), "Viewer", "viewer@example.com", "a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	p.db.SetUserRole(context.Background(), viewer.ID, store.RoleViewer)

	p.do(http.MethodGet, "/login", "", nil).Body.Close()
	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()
	response := p.do(http.MethodPost, "/login", `{"email":"viewer@example.com","password":"a-long-enough-password"}`, headers)
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("signing in = %d", response.StatusCode)
	}

	if response := p.do(http.MethodGet, "/security", "", p.inertia()); response.StatusCode != http.StatusOK {
		t.Errorf("a read-only account cannot look at the Security page: %d", response.StatusCode)
	}

	headers = p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	response = p.do(http.MethodPost, "/settings/users", `{"name":"X","email":"x@example.com","password":"a-long-enough-password","role":"admin"}`, headers)
	response.Body.Close()
	if users, _ := p.db.Users(context.Background()); len(users) != 1 {
		t.Errorf("a read-only account created an account: %d users", len(users))
	}

	response = p.do(http.MethodPost, "/security/protection", `{"feature":"kernel","enabled":true}`, headers)
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther && response.StatusCode != http.StatusFound {
		t.Errorf("a refused change answered %d", response.StatusCode)
	}

	if response := p.do(http.MethodGet, "/terminal", "", p.inertia()); response.StatusCode == http.StatusOK {
		t.Error("a read-only account opened the terminal")
	}
}
