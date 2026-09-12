package redisutil

import (
	"crypto/tls"
	"strings"
	"testing"
)

func TestClientOptions_AddressForms(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantAddr string
		wantUser string
		wantPass string
		wantDB   int
		wantTLS  bool
	}{
		{name: "bare host port", addr: "localhost:6379", wantAddr: "localhost:6379"},
		{name: "bare ipv6", addr: "[::1]:6379", wantAddr: "[::1]:6379"},
		{name: "redis url", addr: "redis://redis:6379", wantAddr: "redis:6379"},
		{name: "tcp alias", addr: "tcp://redis:6379", wantAddr: "redis:6379"},
		{
			// The form the shipped docker config uses; before redisutil this
			// reached go-redis verbatim and failed with "too many colons".
			name: "redis url with credentials and db",
			addr: "redis://alice:s3cret@redis.example.com:6380/3",

			wantAddr: "redis.example.com:6380",
			wantUser: "alice",
			wantPass: "s3cret",
			wantDB:   3,
		},
		{
			// rediss:// used to be scheme-stripped, silently downgrading the
			// connection to plaintext.
			name:     "rediss enables tls",
			addr:     "rediss://cache.example.com:6380",
			wantAddr: "cache.example.com:6380",
			wantTLS:  true,
		},
		{
			name:     "password only",
			addr:     "redis://:justpass@redis:6379",
			wantAddr: "redis:6379",
			wantPass: "justpass",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := ClientOptions(tc.addr, Auth{})
			if err != nil {
				t.Fatalf("ClientOptions(%q): %v", tc.addr, err)
			}
			if opts.Addr != tc.wantAddr {
				t.Errorf("Addr = %q, want %q", opts.Addr, tc.wantAddr)
			}
			if opts.Username != tc.wantUser {
				t.Errorf("Username = %q, want %q", opts.Username, tc.wantUser)
			}
			if opts.Password != tc.wantPass {
				t.Errorf("Password = %q, want %q", opts.Password, tc.wantPass)
			}
			if opts.DB != tc.wantDB {
				t.Errorf("DB = %d, want %d", opts.DB, tc.wantDB)
			}
			if gotTLS := opts.TLSConfig != nil; gotTLS != tc.wantTLS {
				t.Errorf("TLSConfig set = %v, want %v", gotTLS, tc.wantTLS)
			}
		})
	}
}

func TestClientOptions_UnixSocket(t *testing.T) {
	opts, err := ClientOptions("unix:///var/run/redis.sock", Auth{})
	if err != nil {
		t.Fatalf("unix address: %v", err)
	}
	if opts.Network != "unix" {
		t.Errorf("Network = %q, want unix", opts.Network)
	}
	if opts.Addr != "/var/run/redis.sock" {
		t.Errorf("Addr = %q, want /var/run/redis.sock", opts.Addr)
	}
}

func TestClientOptions_AuthOverridesURL(t *testing.T) {
	db := 7
	opts, err := ClientOptions("redis://alice:fromurl@redis:6379/1", Auth{
		Username: "bob",
		Password: "fromconfig",
		DB:       &db,
		TLS:      &tls.Config{MinVersion: tls.VersionTLS13},
	})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts.Username != "bob" || opts.Password != "fromconfig" {
		t.Errorf("credentials = %q/%q, want bob/fromconfig", opts.Username, opts.Password)
	}
	if opts.DB != 7 {
		t.Errorf("DB = %d, want 7", opts.DB)
	}
	if opts.TLSConfig == nil || opts.TLSConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("TLSConfig not applied from Auth")
	}
}

// A nil Auth.DB must leave the URL's database alone; a zero-valued Auth
// must not silently reset it to 0.
func TestClientOptions_ZeroAuthKeepsURLDatabase(t *testing.T) {
	opts, err := ClientOptions("redis://redis:6379/4", Auth{})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts.DB != 4 {
		t.Errorf("DB = %d, want 4 (zero Auth must not override)", opts.DB)
	}
}

func TestClientOptions_ExplicitZeroDatabaseOverrides(t *testing.T) {
	zero := 0
	opts, err := ClientOptions("redis://redis:6379/4", Auth{DB: &zero})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts.DB != 0 {
		t.Errorf("DB = %d, want 0 (explicit zero must override)", opts.DB)
	}
}

func TestClientOptions_Rejections(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantSub string
	}{
		{name: "empty", addr: "   ", wantSub: "empty"},
		{name: "sentinel", addr: "redis+sentinel://a:26379/mymaster", wantSub: "Options.RedisClient"},
		{name: "cluster", addr: "redis+cluster://a:6379", wantSub: "Options.RedisClient"},
		{name: "no port", addr: "localhost", wantSub: "neither host:port nor a redis URL"},
		{name: "unknown scheme", addr: "http://localhost:6379", wantSub: "parse redis address"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ClientOptions(tc.addr, Auth{})
			if err == nil {
				t.Fatalf("ClientOptions(%q) = nil error, want rejection", tc.addr)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	c, err := NewClient("redis://redis:6379/2", Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if got := c.Options().Addr; got != "redis:6379" {
		t.Errorf("Addr = %q, want redis:6379", got)
	}
	if got := c.Options().DB; got != 2 {
		t.Errorf("DB = %d, want 2", got)
	}
}
