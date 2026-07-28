package seat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"seat1", "wohnzimmer", "anna", "seat-two", "a1b"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := map[string]string{
		"seat0":                              "the physical seat, and the one name that breaks input attribution",
		"":                                   "empty",
		"ab":                                 "too short",
		"Seat1":                              "upper case, which Incus and the device tag would disagree about",
		"1seat":                              "starts with a digit",
		"seat_1":                             "underscore",
		"seat 1":                             "space",
		"seat.1":                             "dot",
		"seat-":                              "trailing hyphen",
		"../etc":                             "path traversal, which would escape the state directory",
		"seat1;reboot":                       "shell metacharacters",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "too long",
	}

	for name, why := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted it (%s)", name, why)
		}
	}
}

func TestSeatValidate(t *testing.T) {
	base := Seat{Name: "seat1", Resolution: "1920x1080@60Hz"}

	if err := base.Validate(); err != nil {
		t.Fatalf("a plain seat did not validate: %v", err)
	}

	t.Run("resolution", func(t *testing.T) {
		for _, res := range []string{"", "1920x1080", "1920x1080@60", "60Hz", "x@Hz", "1920*1080@60Hz"} {
			s := base
			s.Resolution = res

			if err := s.Validate(); err == nil {
				t.Errorf("accepted resolution %q", res)
			}
		}

		for _, res := range []string{"1920x1080@60Hz", "2560x1440@120Hz", "3840x2160@60Hz", "800x600@75Hz"} {
			s := base
			s.Resolution = res

			if err := s.Validate(); err != nil {
				t.Errorf("rejected resolution %q: %v", res, err)
			}
		}
	})

	t.Run("static address needs a prefix and a gateway", func(t *testing.T) {
		s := base
		s.Address = "10.20.30.71"
		s.Gateway = "10.20.30.1"

		if err := s.Validate(); err == nil {
			t.Error("accepted an address without a prefix length")
		}

		s.Address = "10.20.30.71/24"
		s.Gateway = ""

		if err := s.Validate(); err == nil {
			t.Error("accepted a static address without a gateway")
		}

		s.Gateway = "10.20.30.1"

		if err := s.Validate(); err != nil {
			t.Errorf("rejected a complete static address: %v", err)
		}
	})

	t.Run("empty address means DHCP and needs no gateway", func(t *testing.T) {
		s := base
		s.Address = ""
		s.Gateway = ""

		if err := s.Validate(); err != nil {
			t.Errorf("rejected a DHCP seat: %v", err)
		}
	})
}

func TestStoreRoundTrip(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	if seats, err := store.List(); err != nil || len(seats) != 0 {
		t.Fatalf("a fresh store listed %v, %v", seats, err)
	}

	// Written out of order to check that List sorts, because an interface that
	// reshuffles its cards between refreshes is unusable.
	for _, name := range []string{"seat2", "anna", "seat1"} {
		if err := store.Put(Seat{Name: name, Resolution: "1920x1080@60Hz"}); err != nil {
			t.Fatalf("Put(%q): %v", name, err)
		}
	}

	seats, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"anna", "seat1", "seat2"}
	if len(seats) != len(want) {
		t.Fatalf("listed %d seats, want %d", len(seats), len(want))
	}

	for i, name := range want {
		if seats[i].Name != name {
			t.Errorf("seat %d is %q, want %q", i, seats[i].Name, name)
		}
	}

	if err := store.Delete("anna"); err != nil {
		t.Errorf("Delete: %v", err)
	}

	// Deleting something that is not there is not an error, so that a failed
	// creation can be cleaned up without checking first.
	if err := store.Delete("anna"); err != nil {
		t.Errorf("second Delete: %v", err)
	}

	if _, err := store.Get("anna"); err == nil {
		t.Error("a deleted seat is still readable")
	}
}

func TestSecrets(t *testing.T) {
	dir := t.TempDir()

	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	// A seat without credentials answers empty rather than failing, because
	// that is the normal state of a seat that has not been provisioned.
	secrets, err := store.Secrets("seat1")
	if err != nil {
		t.Fatalf("Secrets on a fresh seat: %v", err)
	}

	if secrets.SunshineUser != "" {
		t.Errorf("a fresh seat already has credentials: %+v", secrets)
	}

	want := Secrets{SunshineUser: "polyseat", SunshinePassword: "a password"}
	if err := store.PutSecrets("seat1", want); err != nil {
		t.Fatalf("PutSecrets: %v", err)
	}

	got, err := store.Secrets("seat1")
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}

	if got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	// Root only. This is a password for a web interface on the LAN.
	info, err := os.Stat(filepath.Join(dir, "secrets", "seat1.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets are %o, want 600", perm)
	}

	if err := store.DeleteSecrets("seat1"); err != nil {
		t.Errorf("DeleteSecrets: %v", err)
	}

	if got, _ := store.Secrets("seat1"); got.SunshineUser != "" {
		t.Error("the credentials survived deletion")
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}

	for range 32 {
		password, err := RandomPassword()
		if err != nil {
			t.Fatalf("RandomPassword: %v", err)
		}

		if len(password) < 16 {
			t.Fatalf("password is only %d characters: %q", len(password), password)
		}

		if seen[password] {
			t.Fatalf("RandomPassword repeated itself: %q", password)
		}

		seen[password] = true
	}
}

func TestOriginsFor(t *testing.T) {
	got := OriginsFor(map[string][]string{
		"eth1": {"10.20.30.71"},
		"eth0": {"10.54.160.167"},
	})

	// Sorted, because an unsorted list would look different on every refresh
	// and the daemon would keep rewriting a configuration that never changed.
	want := []string{"https://10.20.30.71:47990", "https://10.54.160.167:47990"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("origin %d is %q, want %q", i, got[i], want[i])
		}
	}

	if origins := OriginsFor(nil); len(origins) != 0 {
		t.Errorf("a seat with no addresses produced %v", origins)
	}
}

func TestParseOrigins(t *testing.T) {
	addresses := map[string][]string{"eth1": {"10.20.30.71"}, "eth0": {"10.54.160.167"}}
	want := OriginsFor(addresses)

	// Rendered from the template the daemon actually writes, not from a
	// handwritten snippet. The handwritten version is what let the first
	// parser through: the real file explains itself in a comment that contains
	// the words csrf_allowed_origins, the parser matched that comment first,
	// and every adopted seat looked like its address had moved.
	conf, err := render("assets/sunshine.conf", map[string]string{
		"Origins": strings.Join(want, ","),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(string(conf), "# ") {
		t.Fatal("the template has no comments any more, so this test no longer tests anything")
	}

	got := ParseOrigins(conf)
	if !sameStrings(got, want) {
		t.Errorf("round trip gave %v, want %v", got, want)
	}

	for _, conf := range []string{
		"",
		"capture = wlr\n",
		"csrf_allowed_origins =\n",
		"# csrf_allowed_origins = https://evil:47990\n",
		"  # indented comment mentioning csrf_allowed_origins = nonsense\n",
	} {
		if origins := ParseOrigins([]byte(conf)); len(origins) != 0 {
			t.Errorf("ParseOrigins(%q) = %v, want nothing", conf, origins)
		}
	}
}
