package discovery

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRegistration struct {
	mu        *sync.Mutex
	shutdowns *int
}

func (r *fakeRegistration) Shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.shutdowns++
}

type fakeRegistrar struct {
	mu                      sync.Mutex
	addresses               []string
	shutdowns               int
	err                     error
	afterSecondRegistration context.CancelFunc
}

func (r *fakeRegistrar) Register(cfg Config) (Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	r.addresses = append(r.addresses, cfg.Address.String())
	if len(r.addresses) == 2 && r.afterSecondRegistration != nil {
		r.afterSecondRegistration()
	}
	return &fakeRegistration{mu: &r.mu, shutdowns: &r.shutdowns}, nil
}

type fakeAddressSource struct {
	mu        sync.Mutex
	addresses []net.IP
	err       error
}

type cancellationAddressSource struct {
	entered chan struct{}
}

func (s cancellationAddressSource) Current(ctx context.Context) (net.IP, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func newFakeAddressSource(raw ...string) *fakeAddressSource {
	source := &fakeAddressSource{}
	for _, address := range raw {
		source.addresses = append(source.addresses, net.ParseIP(address))
	}
	return source
}

func (s *fakeAddressSource) Current(context.Context) (net.IP, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if len(s.addresses) == 0 {
		return nil, errors.New("no address")
	}
	address := s.addresses[0]
	if len(s.addresses) > 1 {
		s.addresses = s.addresses[1:]
	}
	return address, nil
}

func TestRunReregistersOnlyWhenAddressChanges(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = time.Second
	addresses := newFakeAddressSource("192.168.1.25", "192.168.1.26")
	registrar := &fakeRegistrar{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	registrar.afterSecondRegistration = cancel

	if err := Run(ctx, cfg, registrar, addresses); err != nil {
		t.Fatal(err)
	}
	if got, want := registrar.addresses, []string{"192.168.1.25", "192.168.1.26"}; !equalStrings(got, want) {
		t.Fatalf("registered addresses = %v, want %v", got, want)
	}
	if registrar.shutdowns != 2 {
		t.Fatalf("shutdowns = %d, want 2", registrar.shutdowns)
	}
}

func TestRunShutsDownOnCancellation(t *testing.T) {
	cfg := validConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registrar := &fakeRegistrar{}

	if err := Run(ctx, cfg, registrar, newFakeAddressSource("192.168.1.25")); err != nil {
		t.Fatal(err)
	}
	if registrar.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", registrar.shutdowns)
	}
}

func TestRunShutsDownOnPollingError(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = time.Second
	source := newFakeAddressSource("192.168.1.25")
	source.err = errors.New("interface unavailable")
	registrar := &fakeRegistrar{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := Run(ctx, cfg, registrar, source); err == nil {
		t.Fatal("Run() error = nil")
	}
	if registrar.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", registrar.shutdowns)
	}
}

func TestRunTreatsCancellationDuringPollAsCleanShutdown(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = time.Second
	registrar := &fakeRegistrar{}
	source := cancellationAddressSource{entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, cfg, registrar, source)
	}()
	<-source.entered
	cancel()

	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if registrar.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", registrar.shutdowns)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestZeroconfRegistrarPublishesOnOwningInterface(t *testing.T) {
	cfg := validConfig()
	var got proxyRegistration
	registrar := &ZeroconfRegistrar{
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return nil, &net.DNSError{IsNotFound: true}
		},
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Index: 7, Name: "Ethernet"}}, nil
		},
		interfaceAddrs: func(net.Interface) ([]net.Addr, error) {
			return []net.Addr{&net.IPNet{IP: cfg.Address, Mask: net.CIDRMask(24, 32)}}, nil
		},
		registerProxy: func(request proxyRegistration) (Registration, error) {
			got = request
			return &fakeRegistration{mu: &sync.Mutex{}, shutdowns: new(int)}, nil
		},
		preflightTimeout: 20 * time.Millisecond,
	}

	registration, err := registrar.Register(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Shutdown()

	if got.Instance != "BookBox" || got.Hostname != "bookbox" ||
		got.Service != Service || got.Domain != Domain || got.Port != 8787 {
		t.Fatalf("registration = %#v", got)
	}
	if !equalStrings(got.Addresses, []string{"192.168.1.25"}) {
		t.Fatalf("addresses = %v", got.Addresses)
	}
	if !equalStrings(got.Text, []string{"version=1", "scheme=http"}) {
		t.Fatalf("text = %v", got.Text)
	}
	if len(got.Interfaces) != 1 || got.Interfaces[0].Index != 7 {
		t.Fatalf("interfaces = %#v", got.Interfaces)
	}
}

func TestZeroconfRegistrarRejectsConflictingHostname(t *testing.T) {
	cfg := validConfig()
	called := false
	registrar := &ZeroconfRegistrar{
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.99")}}, nil
		},
		registerProxy: func(proxyRegistration) (Registration, error) {
			called = true
			return nil, nil
		},
		preflightTimeout: 20 * time.Millisecond,
	}

	_, err := registrar.Register(cfg)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("Register() error = %v", err)
	}
	if called {
		t.Fatal("RegisterProxy called despite hostname conflict")
	}
}

func TestZeroconfRegistrarRejectsAddressWithoutOwningInterface(t *testing.T) {
	cfg := validConfig()
	registrar := &ZeroconfRegistrar{
		lookup: func(context.Context, string) ([]net.IPAddr, error) { return nil, nil },
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Index: 7, Name: "Ethernet"}}, nil
		},
		interfaceAddrs: func(net.Interface) ([]net.Addr, error) {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.2.25"), Mask: net.CIDRMask(24, 32)}}, nil
		},
		preflightTimeout: 20 * time.Millisecond,
	}

	if _, err := registrar.Register(cfg); err == nil || !strings.Contains(err.Error(), "interface") {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestZeroconfRegistrarAllowsItsPreviousAddressDuringReregistration(t *testing.T) {
	cfg := validConfig()
	var lookupAddresses []net.IPAddr
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	registrar := &ZeroconfRegistrar{
		now:            func() time.Time { return now },
		ownershipGrace: time.Minute,
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return lookupAddresses, nil
		},
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Index: 7, Name: "Ethernet"}}, nil
		},
		interfaceAddrs: func(net.Interface) ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("192.168.1.25"), Mask: net.CIDRMask(24, 32)},
				&net.IPNet{IP: net.ParseIP("192.168.1.26"), Mask: net.CIDRMask(24, 32)},
			}, nil
		},
		registerProxy: func(proxyRegistration) (Registration, error) {
			return &fakeRegistration{mu: &sync.Mutex{}, shutdowns: new(int)}, nil
		},
		preflightTimeout: 20 * time.Millisecond,
	}

	first, err := registrar.Register(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first.Shutdown()

	lookupAddresses = []net.IPAddr{{IP: cfg.Address}}
	cfg.Address = net.ParseIP("192.168.1.26")
	second, err := registrar.Register(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second.Shutdown()
}

func TestZeroconfRegistrarExpiresPreviousAddressOwnership(t *testing.T) {
	cfg := validConfig()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	var lookupAddresses []net.IPAddr
	registrar := &ZeroconfRegistrar{
		now:            func() time.Time { return now },
		ownershipGrace: time.Minute,
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return lookupAddresses, nil
		},
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Index: 7, Name: "Ethernet"}}, nil
		},
		interfaceAddrs: func(net.Interface) ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("192.168.1.25"), Mask: net.CIDRMask(24, 32)},
				&net.IPNet{IP: net.ParseIP("192.168.1.26"), Mask: net.CIDRMask(24, 32)},
			}, nil
		},
		registerProxy: func(proxyRegistration) (Registration, error) {
			return &fakeRegistration{mu: &sync.Mutex{}, shutdowns: new(int)}, nil
		},
		preflightTimeout: 20 * time.Millisecond,
	}

	first, err := registrar.Register(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first.Shutdown()

	now = now.Add(2 * time.Minute)
	lookupAddresses = []net.IPAddr{{IP: cfg.Address}}
	cfg.Address = net.ParseIP("192.168.1.26")
	if _, err := registrar.Register(cfg); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("Register() error = %v", err)
	}
}

func TestZeroconfRegistrarSweepsExpiredUnrelatedOwnershipEntries(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	registrar := &ZeroconfRegistrar{
		now:            func() time.Time { return now },
		ownershipGrace: time.Minute,
		ownedAddresses: map[string]time.Time{
			"192.168.1.10": now.Add(-time.Minute),
			"192.168.1.11": now.Add(-time.Second),
			"192.168.1.12": now.Add(time.Minute),
		},
	}

	if registrar.ownsAddress(net.ParseIP("192.168.1.99")) {
		t.Fatal("unowned address reported as owned")
	}
	if len(registrar.ownedAddresses) != 1 {
		t.Fatalf("owned address cache size after access = %d, want 1", len(registrar.ownedAddresses))
	}

	registrar.markAddressOwned(net.ParseIP("192.168.1.25"))
	if len(registrar.ownedAddresses) != 2 {
		t.Fatalf("owned address cache size after insert = %d, want 2", len(registrar.ownedAddresses))
	}
}

func TestInterfaceAddressSourceReturnsCurrentPrivateIPv4(t *testing.T) {
	source := &InterfaceAddressSource{
		Interface:        net.Interface{Index: 7, Name: "Ethernet"},
		preferredAddress: net.ParseIP("192.168.1.26"),
		interfaceAddrs: func(net.Interface) ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("192.168.1.24"), Mask: net.CIDRMask(24, 32)},
				&net.IPNet{IP: net.ParseIP("169.254.1.2"), Mask: net.CIDRMask(16, 32)},
				&net.IPNet{IP: net.ParseIP("192.168.1.26"), Mask: net.CIDRMask(24, 32)},
			}, nil
		},
	}

	address, err := source.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := address.String(); got != "192.168.1.26" {
		t.Fatalf("address = %s", got)
	}
}
