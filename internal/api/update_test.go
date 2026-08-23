package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/superuser404notfound/Polyseat/internal/auth"
)

// claimed is a store with a password already set, which is what every machine
// looks like after somebody has opened the page once.
func claimed(t *testing.T) *auth.Store {
	t.Helper()

	store, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Claim("vincent", "the right one"); err != nil {
		t.Fatal(err)
	}

	return store
}

// ask builds the request the handler would have received. The address matters:
// the rate limiter counts per source, and every test here would otherwise share
// one budget and start failing in whatever order they happen to run.
func ask(body, from string) *http.Request {
	r := httptest.NewRequest("POST", "/api/update", strings.NewReader(body))
	r.RemoteAddr = from + ":40000"

	return r
}

// Off is the default, and off has to mean the body is not read at all: the page
// sends no password when it was not asked for one.
func TestConfirmPasswordIsNotAskedForWhenTheSettingIsOff(t *testing.T) {
	if err := confirmPassword(claimed(t), false, ask("", "10.0.0.1")); err != nil {
		t.Errorf("refused with the setting off: %v", err)
	}
}

func TestConfirmPasswordAcceptsTheRightOne(t *testing.T) {
	err := confirmPassword(claimed(t), true, ask(`{"password":"the right one"}`, "10.0.0.2"))
	if err != nil {
		t.Errorf("refused the right password: %v", err)
	}
}

func TestConfirmPasswordRefusesEveryOtherShapeOfWrong(t *testing.T) {
	cases := []struct {
		why  string
		body string
	}{
		{"a wrong password", `{"password":"the wrong one"}`},
		{"an empty password", `{"password":""}`},
		{"no password field", `{}`},
		{"no body at all", ``},
		{"a body that is not JSON", `the right one`},

		// All three of the above arrive at Check as "", and Check is what
		// refuses them: an empty password cannot be set, because SetPassword
		// enforces MinPasswordLength. Listed anyway, because that is a property
		// of another package which this one relies on, and a change to it
		// should break something here rather than nothing.
		{"a null password", `{"password":null}`},
	}

	store := claimed(t)

	for i, c := range cases {
		// A source of its own per case, so that the failures this deliberately
		// causes do not rate limit the case after it.
		from := "10.0.1." + string(rune('1'+i))

		if err := confirmPassword(store, true, ask(c.body, from)); err == nil {
			t.Errorf("accepted %s", c.why)
		}
	}
}

// The guard has to be the same secret as the login form, not a second one, and
// it has to be told apart from the login form's own answer: this returns "wrong
// password" and never "wrong user name or password", because the user name was
// not asked for.
func TestConfirmPasswordDoesNotAskWhoYouAre(t *testing.T) {
	err := confirmPassword(claimed(t), true, ask(`{"password":"the wrong one"}`, "10.0.0.3"))
	if err == nil {
		t.Fatal("accepted the wrong password")
	}

	if strings.Contains(err.Error(), "user name") {
		t.Errorf("the error mentions a user name that was never asked for: %q", err)
	}
}

// Guessing has to get more expensive, and through the same counter the login
// form uses. Two independent limiters on one secret would give an attacker both
// budgets, and this endpoint is the cheaper one to spend because it needs no
// user name.
func TestConfirmPasswordIsRateLimited(t *testing.T) {
	store := claimed(t)

	var limited bool

	for range 100 {
		err := confirmPassword(store, true, ask(`{"password":"no"}`, "10.0.0.4"))
		if err != nil && strings.Contains(err.Error(), "too many attempts") {
			limited = true

			break
		}
	}

	if !limited {
		t.Error("a hundred wrong passwords from one address were never slowed down")
	}
}
