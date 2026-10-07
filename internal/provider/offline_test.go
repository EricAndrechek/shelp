package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// kindFake is a chainFakeProvider with a configurable Kind.
type kindFake struct {
	chainFakeProvider
	kind Kind
}

func (k *kindFake) Kind() Kind { return k.kind }

func fakeOffline(t *testing.T, offline bool) *int {
	t.Helper()
	n := 0
	prev := offlineCheck
	offlineCheck = func() bool { n++; return !offline }
	resetOffline()
	t.Cleanup(func() { offlineCheck = prev; resetOffline() })
	return &n
}

func TestKindNeedsNetwork(t *testing.T) {
	for k, want := range map[Kind]bool{AppleCloud: true, API: true, AgentCLI: true, OnDevice: false, LocalServer: false, "weird": false} {
		if got := k.NeedsNetwork(); got != want {
			t.Errorf("%s.NeedsNetwork() = %v, want %v", k, got, want)
		}
	}
}

func TestChain_OfflineSkipsNetworkReachesLocal(t *testing.T) {
	setCooldownCacheHome(t)
	calls := fakeOffline(t, true)
	cloud := &kindFake{chainFakeProvider{id: "chainfaketest:cloud", text: "x"}, AppleCloud}
	agent := &kindFake{chainFakeProvider{id: "chainfaketest:agent", text: "x"}, AgentCLI}
	local := &kindFake{chainFakeProvider{id: "chainfaketest:fm", text: "ok"}, OnDevice}
	setChainFakeProviders(t, cloud, agent, local)
	var msgs []string
	// force=true must not override offline.
	res, err := Chain(context.Background(), nil, []string{cloud.id, agent.id, local.id}, Request{}, true, func(id string, err error) {
		msgs = append(msgs, err.Error())
	})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if cloud.callCount()+agent.callCount() != 0 || local.callCount() != 1 {
		t.Fatalf("calls cloud=%d agent=%d local=%d", cloud.callCount(), agent.callCount(), local.callCount())
	}
	if *calls != 1 {
		t.Errorf("offline check ran %d times, want 1", *calls)
	}
	want := "chainfaketest:cloud skipped: offline (no network route)"
	if len(msgs) != 2 || msgs[0] != want {
		t.Errorf("msgs = %q, want first %q", msgs, want)
	}
}

func TestChain_OnlineNeverChecksForLocalOnly(t *testing.T) {
	setCooldownCacheHome(t)
	calls := fakeOffline(t, false)
	local := &kindFake{chainFakeProvider{id: "chainfaketest:fm", text: "ok"}, OnDevice}
	srv := &kindFake{chainFakeProvider{id: "chainfaketest:srv", text: "ok"}, LocalServer}
	setChainFakeProviders(t, local, srv)
	if _, err := Chain(context.Background(), nil, []string{local.id}, Request{}, false, nil); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Errorf("check ran %d times for local-only chain, want 0", *calls)
	}
}

func TestChain_OnlineCallsNetworkProvider(t *testing.T) {
	setCooldownCacheHome(t)
	calls := fakeOffline(t, false)
	cloud := &kindFake{chainFakeProvider{id: "chainfaketest:cloud", text: "ok"}, AppleCloud}
	api := &kindFake{chainFakeProvider{id: "chainfaketest:api", text: "ok"}, API}
	setChainFakeProviders(t, cloud, api)
	if _, err := Chain(context.Background(), nil, []string{cloud.id, api.id}, Request{}, false, nil); err != nil {
		t.Fatal(err)
	}
	if cloud.callCount() != 1 || *calls != 1 {
		t.Errorf("cloud calls=%d check=%d", cloud.callCount(), *calls)
	}
}

func TestChain_AllNetworkOfflineHelpfulError(t *testing.T) {
	setCooldownCacheHome(t)
	fakeOffline(t, true)
	cloud := &kindFake{chainFakeProvider{id: "chainfaketest:cloud", text: "x"}, AppleCloud}
	setChainFakeProviders(t, cloud)
	_, err := Chain(context.Background(), nil, []string{cloud.id}, Request{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "offline, and no local provider") || !strings.Contains(err.Error(), "`fm`") {
		t.Fatalf("err = %v", err)
	}
	var ose *OfflineSkippedError
	var ue *UnavailableError
	if !errors.As(err, &ose) || !errors.As(err, &ue) {
		t.Errorf("want OfflineSkippedError and UnavailableError in %v", err)
	}
}

func TestChain_NetworkErrorMarksRestOffline(t *testing.T) {
	setCooldownCacheHome(t)
	fakeOffline(t, false)
	a := &kindFake{chainFakeProvider{id: "chainfaketest:a", err: Unavailable("chainfaketest:a", "could not reach https://x/v1: dial tcp: lookup x: no such host")}, API}
	b := &kindFake{chainFakeProvider{id: "chainfaketest:b", text: "x"}, AppleCloud}
	c := &kindFake{chainFakeProvider{id: "chainfaketest:c", text: "ok"}, OnDevice}
	setChainFakeProviders(t, a, b, c)
	res, err := Chain(context.Background(), nil, []string{a.id, b.id, c.id}, Request{}, false, nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if b.callCount() != 0 {
		t.Error("b should have been skipped after a's DNS failure")
	}
}

func TestChain_NetworkErrorFromNonAPINotClassified(t *testing.T) {
	setCooldownCacheHome(t)
	fakeOffline(t, false)
	a := &kindFake{chainFakeProvider{id: "chainfaketest:a", err: errors.New("no such host")}, AgentCLI}
	b := &kindFake{chainFakeProvider{id: "chainfaketest:b", text: "ok"}, AppleCloud}
	setChainFakeProviders(t, a, b)
	if _, err := Chain(context.Background(), nil, []string{a.id, b.id}, Request{}, false, nil); err != nil {
		t.Fatal(err)
	}
	if b.callCount() != 1 {
		t.Error("agent-cli text errors must not mark the chain offline")
	}
}

func TestIsNetworkError(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"dns text":    {fmt.Errorf("Post: dial tcp: lookup api.x.com: no such host"), true},
		"unreachable": {errors.New("dial tcp 1.2.3.4:443: connect: network is unreachable"), true},
		"no route":    {errors.New("connect: no route to host"), true},
		"tls timeout": {errors.New("net/http: TLS handshake timeout"), true},
		"refused":     {errors.New("dial tcp 10.0.0.5:443: connect: connection refused"), false},
		"plain 500":   {errors.New("unexpected status 500"), false},
		"nil":         {nil, false},
		"deadline":    {context.DeadlineExceeded, false},
	}
	for name, c := range cases {
		if got := IsNetworkError(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func TestHasRouteDoesNotPanic(t *testing.T) { _ = hasRoute() }
