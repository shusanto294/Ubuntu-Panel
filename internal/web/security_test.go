package web_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/totp"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "a-long-enough-password"
)

// signedOut creates the first account and signs it out again, leaving a
// fresh session at the sign-in form.
func signedOut(t *testing.T) *panel {
	t.Helper()

	p := start(t)

	p.do(http.MethodGet, "/setup", "", nil).Body.Close()

	response := p.post("/setup", `{"name":"Admin","email":"`+adminEmail+`",
		"password":"`+adminPassword+`","password_confirmation":"`+adminPassword+`"}`)
	response.Body.Close()

	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup = %d", response.StatusCode)
	}

	p.post("/logout", "").Body.Close()
	p.do(http.MethodGet, "/login", "", nil).Body.Close()

	return p
}

func (p *panel) post(path, body string) *http.Response {
	headers := p.inertia()
	headers["X-XSRF-TOKEN"] = p.csrf()

	return p.do(http.MethodPost, path, body, headers)
}

func (p *panel) login(password string) *http.Response {
	return p.post("/login", `{"email":"`+adminEmail+`","password":"`+password+`"}`)
}

func TestRepeatedFailedSignInsLockTheAddressOut(t *testing.T) {
	p := signedOut(t)

	for i := 0; i < 5; i++ {
		response := p.login("wrong-password-" + string(rune('a'+i)))
		response.Body.Close()

		if location := response.Header.Get("Location"); location == "/dashboard" {
			t.Fatalf("attempt %d with a wrong password signed in", i+1)
		}
	}

	// The right password, from an address that is now over the limit, is
	// refused without being checked.
	response := p.login(adminPassword)
	response.Body.Close()

	if location := response.Header.Get("Location"); location == "/dashboard" {
		t.Fatal("a locked-out address signed in")
	}

	attempts, err := p.db.LoginAttempts(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(attempts) != 6 || attempts[0].Reason != store.LoginLocked || attempts[0].Success {
		t.Fatalf("expected five failures and a locked attempt, got %+v", attempts)
	}

	// Unlocking clears it.
	p.db.ClearLoginFailures(context.Background(), attempts[0].IP)

	response = p.login(adminPassword)
	response.Body.Close()

	if location := response.Header.Get("Location"); location != "/dashboard" {
		t.Fatalf("after unlocking, the right password redirected to %q", location)
	}
}

func TestTwoFactorIsRequiredAfterThePassword(t *testing.T) {
	p := signedOut(t)
	ctx := context.Background()

	secret := totp.NewSecret()

	if err := p.db.StartTwoFactor(ctx, 1, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ConfirmTwoFactor(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}

	response := p.login(adminPassword)
	response.Body.Close()

	if location := response.Header.Get("Location"); location != "/login/two-factor" {
		t.Fatalf("a right password with two-factor on redirected to %q", location)
	}

	// Not signed in yet.
	response = p.do(http.MethodGet, "/dashboard", "", p.inertia())
	response.Body.Close()

	if response.StatusCode == http.StatusOK {
		t.Fatal("the dashboard was served before the second step")
	}

	response = p.post("/login/two-factor", `{"code":"000000"}`)
	response.Body.Close()

	if location := response.Header.Get("Location"); location == "/dashboard" {
		t.Fatal("a wrong code signed in")
	}

	code, _ := totp.Code(secret, time.Now())

	response = p.post("/login/two-factor", `{"code":"`+code+`"}`)
	response.Body.Close()

	if location := response.Header.Get("Location"); location != "/dashboard" {
		t.Fatalf("the right code redirected to %q", location)
	}

	// The same code a second time, from another browser, is refused.
	other := *p
	other.client = cloneClient(t, p)
	other.do(http.MethodGet, "/login", "", nil).Body.Close()

	response = other.login(adminPassword)
	response.Body.Close()

	response = other.post("/login/two-factor", `{"code":"`+code+`"}`)
	response.Body.Close()

	if location := response.Header.Get("Location"); location == "/dashboard" {
		t.Fatal("a code was accepted twice")
	}
}

func TestRecoveryCodesWorkOnce(t *testing.T) {
	p := signedOut(t)
	ctx := context.Background()

	p.db.StartTwoFactor(ctx, 1, totp.NewSecret())

	codes, err := p.db.ConfirmTwoFactor(ctx, 1, 0)
	if err != nil || len(codes) != 8 {
		t.Fatalf("recovery codes: %v %v", codes, err)
	}

	if !p.db.UseRecoveryCode(ctx, 1, strings.ToUpper(codes[3])) {
		t.Fatal("a recovery code was refused")
	}

	if p.db.UseRecoveryCode(ctx, 1, codes[3]) {
		t.Fatal("a recovery code was accepted twice")
	}

	factor, _ := p.db.TwoFactorFor(ctx, 1)
	if len(factor.Recovery) != 7 {
		t.Fatalf("expected 7 codes left, got %d", len(factor.Recovery))
	}
}

func TestResponsesCarrySecurityHeaders(t *testing.T) {
	p := start(t)

	response := p.do(http.MethodGet, "/login", "", nil)
	response.Body.Close()

	for name, want := range map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := response.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// cloneClient is a second browser against the same server: the same
// settings, an empty cookie jar.
func cloneClient(t *testing.T, p *panel) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	client := *p.client
	client.Jar = jar

	return &client
}
