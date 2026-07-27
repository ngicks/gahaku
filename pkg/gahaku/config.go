package gahaku

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is the materialized configuration the binary runs with, after every
// layer (defaults < file < environment < flags) is applied.
type Config struct {
	// Listen is the address the gRPC server binds.
	Listen string `json:"listen" yaml:"listen"`
	// ShutdownTimeout bounds the wait for in-flight renders once the server is
	// asked to stop; the renders still running when it passes are killed.
	// Serialized as nanoseconds in a config file, as a duration string
	// ("30s") in the environment.
	ShutdownTimeout time.Duration `json:"shutdown_timeout" yaml:"shutdown_timeout"`
	// TempDir is where per-job directories are created. Empty selects the
	// operating system's temp directory.
	TempDir string `json:"temp_dir" yaml:"temp_dir"`
	// Soffice is the LibreOffice binary the office pipeline converts with. The
	// server exports it to its workers, which is how a subprocess started
	// without arguments learns about it.
	Soffice string `json:"soffice" yaml:"soffice"`
	Input   Input  `json:"input" yaml:"input"`
	Worker  Worker `json:"worker" yaml:"worker"`
}

// Input configures where render input may come from.
type Input struct {
	// MaxStreamBytes caps the cumulative size of client-streamed input. 0
	// selects the service default, a negative value disables the cap.
	MaxStreamBytes int64 `json:"max_stream_bytes" yaml:"max_stream_bytes"`
	// MaxFetchBytes caps the body of a presigned GET. 0 disables the cap.
	MaxFetchBytes int64 `json:"max_fetch_bytes" yaml:"max_fetch_bytes"`
	// LocalRoots are the directories a local input path may resolve under.
	// Empty disables local-path input entirely.
	LocalRoots []string `json:"local_roots" yaml:"local_roots"`
	// AllowHttp accepts plain http:// presigned urls in both directions;
	// https-only otherwise.
	AllowHttp bool `json:"allow_http" yaml:"allow_http"`
	// BlockPrivateAddresses refuses presigned urls that resolve into the
	// server's own network.
	BlockPrivateAddresses bool `json:"block_private_addresses" yaml:"block_private_addresses"`
}

// Worker configures the subprocess orchestrator.
type Worker struct {
	// Concurrency caps how many render workers run at once. 0 or less selects
	// one per CPU.
	Concurrency int `json:"concurrency" yaml:"concurrency"`
	// Timeout bounds a single render job. Serialized as nanoseconds in a
	// config file, as a duration string ("2m") in the environment.
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
}

// DefaultShutdownTimeout bounds the graceful stop before running renders are
// killed. It sits above the default job timeout so an ordinary job finishes.
const DefaultShutdownTimeout = 3 * time.Minute

// DefaultConfig is the lowest-precedence layer.
func DefaultConfig() Config {
	return Config{
		Listen:          ":9000",
		ShutdownTimeout: DefaultShutdownTimeout,
		Input: Input{
			// Deny-by-default everywhere it matters: no local roots, no plain
			// http, and no reaching into the network the server sits in.
			BlockPrivateAddresses: true,
		},
		Worker: Worker{Timeout: 2 * time.Minute},
	}
}

// PartialConfig is the sparse mirror of Config: a nil field means "absent,
// leave the lower layer", a set one is an explicit value including an explicit
// zero. It is the decode target of the config file and the struct the
// environment is parsed into, so both layers merge through one Apply.
//
//nolint:lll // triple json/yaml/env tags; one field per line, never wrap tags
type PartialConfig struct {
	Listen          *string        `json:"listen,omitzero" yaml:"listen,omitempty" env:"LISTEN"`
	ShutdownTimeout *time.Duration `json:"shutdown_timeout,omitzero" yaml:"shutdown_timeout,omitempty" env:"SHUTDOWN_TIMEOUT"`
	TempDir         *string        `json:"temp_dir,omitzero" yaml:"temp_dir,omitempty" env:"TEMP_DIR"`
	Soffice         *string        `json:"soffice,omitzero" yaml:"soffice,omitempty" env:"SOFFICE"`
	Input           PartialInput   `json:"input,omitzero" yaml:"input,omitempty" envPrefix:"INPUT_"`
	Worker          PartialWorker  `json:"worker,omitzero" yaml:"worker,omitempty" envPrefix:"WORKER_"`
}

//nolint:lll // triple json/yaml/env tags; one field per line, never wrap tags
type PartialInput struct {
	MaxStreamBytes        *int64   `json:"max_stream_bytes,omitzero" yaml:"max_stream_bytes,omitempty" env:"MAX_STREAM_BYTES"`
	MaxFetchBytes         *int64   `json:"max_fetch_bytes,omitzero" yaml:"max_fetch_bytes,omitempty" env:"MAX_FETCH_BYTES"`
	LocalRoots            []string `json:"local_roots,omitzero" yaml:"local_roots,omitempty" env:"LOCAL_ROOTS"`
	AllowHttp             *bool    `json:"allow_http,omitzero" yaml:"allow_http,omitempty" env:"ALLOW_HTTP"`
	BlockPrivateAddresses *bool    `json:"block_private_addresses,omitzero" yaml:"block_private_addresses,omitempty" env:"BLOCK_PRIVATE_ADDRESSES"`
}

//nolint:lll // triple json/yaml/env tags; one field per line, never wrap tags
type PartialWorker struct {
	Concurrency *int           `json:"concurrency,omitzero" yaml:"concurrency,omitempty" env:"CONCURRENCY"`
	Timeout     *time.Duration `json:"timeout,omitzero" yaml:"timeout,omitempty" env:"TIMEOUT"`
}

// Apply overlays p's present fields onto base and returns the merged Config.
func (p PartialConfig) Apply(base Config) Config {
	if p.Listen != nil {
		base.Listen = *p.Listen
	}
	if p.ShutdownTimeout != nil {
		base.ShutdownTimeout = *p.ShutdownTimeout
	}
	if p.TempDir != nil {
		base.TempDir = *p.TempDir
	}
	if p.Soffice != nil {
		base.Soffice = *p.Soffice
	}
	base.Input = p.Input.Apply(base.Input)
	base.Worker = p.Worker.Apply(base.Worker)
	return base
}

func (p PartialInput) Apply(base Input) Input {
	if p.MaxStreamBytes != nil {
		base.MaxStreamBytes = *p.MaxStreamBytes
	}
	if p.MaxFetchBytes != nil {
		base.MaxFetchBytes = *p.MaxFetchBytes
	}
	if p.LocalRoots != nil {
		base.LocalRoots = p.LocalRoots
	}
	if p.AllowHttp != nil {
		base.AllowHttp = *p.AllowHttp
	}
	if p.BlockPrivateAddresses != nil {
		base.BlockPrivateAddresses = *p.BlockPrivateAddresses
	}
	return base
}

func (p PartialWorker) Apply(base Worker) Worker {
	if p.Concurrency != nil {
		base.Concurrency = *p.Concurrency
	}
	if p.Timeout != nil {
		base.Timeout = *p.Timeout
	}
	return base
}

// envPrefix opens every variable this package reads. It is also what a worker
// subprocess is configured through, since the orchestrator starts it with no
// arguments of its own (Serve).
const envPrefix = "GAHAKU_"

// SofficeEnvVar carries the resolved LibreOffice binary from the server to the
// workers it spawns.
const SofficeEnvVar = envPrefix + "SOFFICE"

// envOptions configures caarlos0/env for the env layer in LoadConfig; the
// variable names live in PartialConfig's env tags.
var envOptions = env.Options{Prefix: envPrefix}

// LoadConfig assembles defaults < config file < environment through Apply. The
// ./cmd layer applies explicitly-set flags on top. flagPath is the --config
// value, empty when the flag is unset.
func LoadConfig(flagPath string) (Config, error) {
	cfg := DefaultConfig()

	path, err := configPath(flagPath)
	if err != nil {
		return cfg, err
	}
	filePartial, err := unmarshalConfigFile(path)
	if err != nil {
		return cfg, err
	}
	cfg = filePartial.Apply(cfg)

	var envPartial PartialConfig
	if err := env.ParseWithOptions(&envPartial, envOptions); err != nil {
		return cfg, err
	}
	cfg = envPartial.Apply(cfg)

	return cfg, nil
}

// unmarshalConfigFile reads and decodes into a fresh zero PartialConfig; it
// never merges. An absent file is not an error, a malformed one is.
func unmarshalConfigFile(path string) (PartialConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return PartialConfig{}, nil
		}
		return PartialConfig{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var p PartialConfig
	if err := json.Unmarshal(b, &p); err != nil {
		return PartialConfig{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	return p, nil
}

// envConfVar names the config-file-path override, the one variable read by
// hand: the path is needed before anything can be parsed.
const envConfVar = envPrefix + "CONF"

// configPath resolves the file path: --config, else $GAHAKU_CONF, else
// os.UserConfigDir()/gahaku/config.json.
func configPath(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	if p, ok := os.LookupEnv(envConfVar); ok {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gahaku", "config.json"), nil
}
