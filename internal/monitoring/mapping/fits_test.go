package mapping

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

func TestFits(t *testing.T) {
	ok := monitoring.Target{EntityID: strings.Repeat("k", 64), Kind: "vm", Name: strings.Repeat("é", 255),
		Extra: map[string]string{strings.Repeat("a", 64): strings.Repeat("v", 255)}}
	if err := fits(ok); err != nil {
		t.Errorf("target at the column sizes: %v", err)
	}
	for name, mutate := range map[string]func(*monitoring.Target){
		"long key":        func(t *monitoring.Target) { t.EntityID += "k" },
		"long name":       func(t *monitoring.Target) { t.Name += "é" },
		"long attribute":  func(t *monitoring.Target) { t.Extra[strings.Repeat("b", 65)] = "v" },
		"long attr value": func(t *monitoring.Target) { t.Extra["x"] = strings.Repeat("v", 256) },
	} {
		target := ok
		target.Extra = map[string]string{}
		for k, v := range ok.Extra {
			target.Extra[k] = v
		}
		mutate(&target)
		if err := fits(target); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: err = %v, want ErrInvalidTarget", name, err)
		}
	}
}

func TestDSNTimeouts(t *testing.T) {
	cfg, err := mysql.ParseDSN(DBConfig{Host: "db", Port: 3306, Name: "aio11y", User: "u", ReadTimeout: time.Minute}.dsn())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != DefaultConnectTimeout || cfg.ReadTimeout != time.Minute || cfg.WriteTimeout != DefaultWriteTimeout {
		t.Errorf("timeouts = %v / %v / %v", cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout)
	}
}
