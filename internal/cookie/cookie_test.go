package cookie

import (
	"net/http"
	"testing"
)

func TestWiderReportsADomainTheBerthsHostIsUnder(t *testing.T) {
	for _, domain := range []string{".myapp.localhost", "myapp.localhost", "localhost", ".MyApp.Localhost"} {
		if !Wider("feat-x.myapp.localhost", &http.Cookie{Name: "s", Domain: domain}) {
			t.Errorf("Domain=%s is not reported as wider than feat-x.myapp.localhost", domain)
		}
	}
}

func TestWiderAcceptsTheBerthsHostANameUnderItAndHostOnly(t *testing.T) {
	for _, domain := range []string{"", "feat-x.myapp.localhost", ".feat-x.myapp.localhost", "app.feat-x.myapp.localhost", "feat-x.myapp.localhost."} {
		if Wider("feat-x.myapp.localhost", &http.Cookie{Name: "s", Domain: domain}) {
			t.Errorf("Domain=%q is reported as wider than feat-x.myapp.localhost", domain)
		}
	}
}

func TestWiderIgnoresADomainThatOnlySharesASuffix(t *testing.T) {
	for _, domain := range []string{"x.myapp.localhost", "at-x.myapp.localhost", "other.localhost"} {
		if Wider("feat-x.myapp.localhost", &http.Cookie{Name: "s", Domain: domain}) {
			t.Errorf("Domain=%s, which feat-x.myapp.localhost is not under, is reported as wider", domain)
		}
	}
}
