package discovery

import (
	"net"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Hostname:     "bookbox.local.",
		Service:      "_bookbox._tcp",
		Domain:       "local.",
		Address:      net.ParseIP("192.168.1.25"),
		Port:         8787,
		PollInterval: 5 * time.Second,
	}
}

func TestConfigValidateAcceptsPrivateIPv4(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*Config)
		message string
	}{
		{"hostname", func(cfg *Config) { cfg.Hostname = "other.local." }, "hostname"},
		{"service", func(cfg *Config) { cfg.Service = "_http._tcp" }, "service"},
		{"domain", func(cfg *Config) { cfg.Domain = "example." }, "domain"},
		{"zero port", func(cfg *Config) { cfg.Port = 0 }, "port"},
		{"large port", func(cfg *Config) { cfg.Port = 65536 }, "port"},
		{"public address", func(cfg *Config) { cfg.Address = net.ParseIP("8.8.8.8") }, "private"},
		{"loopback address", func(cfg *Config) { cfg.Address = net.ParseIP("127.0.0.1") }, "private"},
		{"IPv6 address", func(cfg *Config) { cfg.Address = net.ParseIP("fd00::1") }, "IPv4"},
		{"short poll interval", func(cfg *Config) { cfg.PollInterval = time.Second - 1 }, "poll interval"},
		{"long poll interval", func(cfg *Config) { cfg.PollInterval = 5*time.Minute + 1 }, "poll interval"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Validate() error = %v, want message containing %q", err, test.message)
			}
		})
	}
}
