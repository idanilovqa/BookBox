package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/enbility/zeroconf/v3"
)

type Registration interface {
	Shutdown()
}

type Registrar interface {
	Register(Config) (Registration, error)
}

type AddressSource interface {
	Current(context.Context) (net.IP, error)
}

type proxyRegistration struct {
	Instance   string
	Service    string
	Domain     string
	Port       int
	Hostname   string
	Addresses  []string
	Text       []string
	Interfaces []net.Interface
}

type ZeroconfRegistrar struct {
	mu               sync.Mutex
	ownedAddresses   map[string]time.Time
	now              func() time.Time
	ownershipGrace   time.Duration
	lookup           func(context.Context, string) ([]net.IPAddr, error)
	interfaces       func() ([]net.Interface, error)
	interfaceAddrs   func(net.Interface) ([]net.Addr, error)
	registerProxy    func(proxyRegistration) (Registration, error)
	preflightTimeout time.Duration
}

func NewZeroconfRegistrar() *ZeroconfRegistrar {
	return &ZeroconfRegistrar{}
}

func (r *ZeroconfRegistrar) Register(cfg Config) (Registration, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	lookup := r.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	timeout := r.preflightTimeout
	if timeout <= 0 {
		timeout = time.Second
	}
	lookupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	addresses, lookupErr := lookup(lookupCtx, cfg.Hostname)
	cancel()
	if lookupErr != nil && !errors.Is(lookupErr, context.DeadlineExceeded) {
		var dnsErr *net.DNSError
		if !errors.As(lookupErr, &dnsErr) || !dnsErr.IsNotFound {
			return nil, fmt.Errorf("preflight lookup %s: %w", cfg.Hostname, lookupErr)
		}
	}
	for _, resolved := range addresses {
		if !cfg.Address.Equal(resolved.IP) && !r.ownsAddress(resolved.IP) {
			return nil, fmt.Errorf(
				"hostname conflict: %s already resolves to %s",
				cfg.Hostname,
				resolved.IP,
			)
		}
	}

	owningInterface, err := r.findOwningInterface(cfg.Address)
	if err != nil {
		return nil, err
	}

	proxyHostname := strings.TrimSuffix(cfg.Hostname, cfg.Domain)
	proxyHostname = strings.TrimSuffix(proxyHostname, ".")
	if proxyHostname == "" {
		return nil, errors.New("zeroconf proxy hostname label is empty")
	}
	request := proxyRegistration{
		Instance:   "BookBox",
		Service:    cfg.Service,
		Domain:     cfg.Domain,
		Port:       cfg.Port,
		Hostname:   proxyHostname,
		Addresses:  []string{cfg.Address.String()},
		Text:       []string{"version=1", "scheme=http"},
		Interfaces: []net.Interface{owningInterface},
	}
	register := r.registerProxy
	if register == nil {
		register = func(request proxyRegistration) (Registration, error) {
			return zeroconf.RegisterProxy(
				request.Instance,
				request.Service,
				request.Domain,
				request.Port,
				request.Hostname,
				request.Addresses,
				request.Text,
				request.Interfaces,
			)
		}
	}
	registration, err := register(request)
	if err != nil {
		return nil, err
	}
	if registration == nil {
		return nil, errors.New("zeroconf registration is nil")
	}
	r.markAddressOwned(cfg.Address)
	return registration, nil
}

func (r *ZeroconfRegistrar) ownsAddress(address net.IP) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.currentTime()
	r.sweepExpiredAddresses(now)
	expiry, ok := r.ownedAddresses[address.String()]
	return ok && now.Before(expiry)
}

func (r *ZeroconfRegistrar) markAddressOwned(address net.IP) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.currentTime()
	r.sweepExpiredAddresses(now)
	if r.ownedAddresses == nil {
		r.ownedAddresses = make(map[string]time.Time)
	}
	grace := r.ownershipGrace
	if grace <= 0 {
		// mDNS records commonly remain cached after Shutdown; bound the
		// self-ownership exemption to one conventional 120-second TTL.
		grace = 2 * time.Minute
	}
	r.ownedAddresses[address.String()] = now.Add(grace)
}

func (r *ZeroconfRegistrar) sweepExpiredAddresses(now time.Time) {
	for address, expiry := range r.ownedAddresses {
		if !now.Before(expiry) {
			delete(r.ownedAddresses, address)
		}
	}
}

func (r *ZeroconfRegistrar) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *ZeroconfRegistrar) findOwningInterface(address net.IP) (net.Interface, error) {
	interfaces := r.interfaces
	if interfaces == nil {
		interfaces = net.Interfaces
	}
	interfaceAddrs := r.interfaceAddrs
	if interfaceAddrs == nil {
		interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) {
			return iface.Addrs()
		}
	}
	return findOwningInterface(address, interfaces, interfaceAddrs)
}

type InterfaceAddressSource struct {
	Interface        net.Interface
	preferredAddress net.IP
	interfaceAddrs   func(net.Interface) ([]net.Addr, error)
}

func NewInterfaceAddressSource(address net.IP) (*InterfaceAddressSource, error) {
	iface, err := findOwningInterface(
		address,
		net.Interfaces,
		func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() },
	)
	if err != nil {
		return nil, err
	}
	return &InterfaceAddressSource{
		Interface:        iface,
		preferredAddress: append(net.IP(nil), address.To4()...),
	}, nil
}

func (s *InterfaceAddressSource) Current(ctx context.Context) (net.IP, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	interfaceAddrs := s.interfaceAddrs
	if interfaceAddrs == nil {
		interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) {
			return iface.Addrs()
		}
	}
	addresses, err := interfaceAddrs(s.Interface)
	if err != nil {
		return nil, err
	}
	var candidates []net.IP
	for _, address := range addresses {
		ip := ipFromAddr(address)
		if ip != nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() {
			candidate := append(net.IP(nil), ip.To4()...)
			if candidate.Equal(s.preferredAddress) {
				return candidate, nil
			}
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) > 0 {
		s.preferredAddress = append(net.IP(nil), candidates[0]...)
		return candidates[0], nil
	}
	return nil, fmt.Errorf("interface %q has no private non-loopback IPv4 address", s.Interface.Name)
}

func findOwningInterface(
	address net.IP,
	interfaces func() ([]net.Interface, error),
	interfaceAddrs func(net.Interface) ([]net.Addr, error),
) (net.Interface, error) {
	all, err := interfaces()
	if err != nil {
		return net.Interface{}, fmt.Errorf("list interfaces: %w", err)
	}
	for _, iface := range all {
		addresses, err := interfaceAddrs(iface)
		if err != nil {
			return net.Interface{}, fmt.Errorf("list addresses for interface %q: %w", iface.Name, err)
		}
		for _, candidate := range addresses {
			if address.Equal(ipFromAddr(candidate)) {
				return iface, nil
			}
		}
	}
	return net.Interface{}, fmt.Errorf("no network interface owns address %s", address)
}

func ipFromAddr(address net.Addr) net.IP {
	switch typed := address.(type) {
	case *net.IPNet:
		return typed.IP
	case *net.IPAddr:
		return typed.IP
	default:
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			return net.ParseIP(address.String())
		}
		return ip
	}
}

func Run(ctx context.Context, cfg Config, registrar Registrar, current AddressSource) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	active, err := registrar.Register(cfg)
	if err != nil {
		return fmt.Errorf("register discovery service: %w", err)
	}
	defer func() {
		if active != nil {
			active.Shutdown()
		}
	}()

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			address, err := current.Current(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("read interface address: %w", err)
			}
			if cfg.Address.Equal(address) {
				continue
			}

			next := cfg
			next.Address = append(net.IP(nil), address...)
			if err := next.Validate(); err != nil {
				return fmt.Errorf("validate changed interface address: %w", err)
			}

			active.Shutdown()
			active = nil
			active, err = registrar.Register(next)
			if err != nil {
				return fmt.Errorf("re-register discovery service: %w", err)
			}
			cfg = next
		}
	}
}
