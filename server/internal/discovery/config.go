package discovery

import (
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	Hostname = "bookbox.local."
	Service  = "_bookbox._tcp"
	Domain   = "local."
)

type Config struct {
	Hostname     string
	Service      string
	Domain       string
	Address      net.IP
	Port         int
	PollInterval time.Duration
}

func (cfg Config) Validate() error {
	if cfg.Hostname != Hostname {
		return fmt.Errorf("hostname must be %q", Hostname)
	}
	if cfg.Service != Service {
		return fmt.Errorf("service must be %q", Service)
	}
	if cfg.Domain != Domain {
		return fmt.Errorf("domain must be %q", Domain)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	address := cfg.Address.To4()
	if address == nil {
		return errors.New("address must be IPv4")
	}
	if !address.IsPrivate() || address.IsLoopback() {
		return errors.New("address must be a private non-loopback IPv4 address")
	}
	if cfg.PollInterval < time.Second || cfg.PollInterval > 5*time.Minute {
		return errors.New("poll interval must be between 1s and 5m")
	}
	return nil
}
