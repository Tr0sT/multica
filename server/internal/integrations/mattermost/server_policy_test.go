package mattermost

import (
	"context"
	"errors"
	"testing"
)

// HTTP is only available to local test fixtures, never NewServerPolicy.
func testServerPolicy(base string) *ServerPolicy {
	return &ServerPolicy{servers: map[string]struct{}{base: {}}}
}

func TestServerPolicyRejectsUnapprovedDestinations(t *testing.T) {
	policy, err := NewServerPolicy("https://MM.example.com:443/mattermost/,https://internal.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"https://mm.example.com/mattermost", "https://internal.example.com/",
	} {
		if _, err := policy.validate(address); err != nil {
			t.Errorf("allowed %s: %v", address, err)
		}
	}
	for _, address := range []string{
		"http://mm.example.com/mattermost", "http://127.0.0.1:8080", "https://127.0.0.1",
		"https://mm.example.com.evil.test/mattermost", "https://mm.example.com:444/mattermost",
		"https://mm.example.com/other", "https://mm.example.com/mattermost/../admin",
		"https://mm.example.com/mattermost%3Fignored", "https://user:pass@mm.example.com/mattermost",
		"https://mm.example.com/mattermost?other", "https://mm.example.com/mattermost#fragment",
	} {
		if _, err := policy.validate(address); err == nil {
			t.Errorf("accepted %s", address)
		}
	}
	var missing *ServerPolicy
	if _, err := missing.validate("https://internal.example.com"); !errors.Is(err, ErrServerNotAllowed) {
		t.Fatalf("nil policy did not fail closed: %v", err)
	}
}

func TestOperatorPolicyRequiresHTTPS(t *testing.T) {
	for _, value := range []string{"", "http://localhost:8065", "https://ok.test,http://bad.test", "https://ok.test,", "https://user:pass@ok.test"} {
		if _, err := NewServerPolicy(value); err == nil {
			t.Errorf("accepted policy %q", value)
		}
	}
}

func TestCanonicalServerURLPreservesEscapedDelimiters(t *testing.T) {
	got, err := canonicalServerURL("https://mm.example.com/path%3Fquery%23fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://mm.example.com/path%3Fquery%23fragment" {
		t.Fatalf("changed URL meaning: %s", got)
	}
}

func TestInstallRejectsDestinationBeforeUsingNetworkOrDatabase(t *testing.T) {
	policy, err := NewServerPolicy("https://approved.test")
	if err != nil {
		t.Fatal(err)
	}
	svc := &InstallService{policy: policy}
	_, err = svc.Register(context.Background(), RegisterParams{ServerURL: "http://127.0.0.1:8000", AccessToken: "token"})
	if !errors.Is(err, ErrServerNotAllowed) {
		t.Fatalf("unapproved destination: %v", err)
	}
}
