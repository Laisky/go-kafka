package dependencysecurity_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"errors"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func TestSettingsConsumerContract(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "settings.yml")
	body := "dry: false\napp: synthetic-consumer\nlabels: [alpha, beta]\nhttp:\n  listen: 127.0.0.1:12345\n  timeout: 2s\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if viper.GetBool("dry") {
		t.Fatal("file bool changed")
	}
	if viper.GetString("APP") != "synthetic-consumer" {
		t.Fatal("case-insensitive key changed")
	}
	if got := viper.GetStringSlice("labels"); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("list changed: %v", got)
	}
	if viper.GetDuration("http.timeout") != 2*time.Second {
		t.Fatal("duration changed")
	}
	flags := pflag.NewFlagSet("synthetic", pflag.ContinueOnError)
	flags.Bool("dry", false, "synthetic flag")
	if err := flags.Parse([]string{"--dry"}); err != nil {
		t.Fatal(err)
	}
	if err := viper.BindPFlags(flags); err != nil {
		t.Fatal(err)
	}
	if !viper.GetBool("dry") {
		t.Fatal("explicit flag did not override file")
	}
	var config struct {
		Dry  bool   `mapstructure:"dry"`
		App  string `mapstructure:"app"`
		HTTP struct {
			Listen  string
			Timeout time.Duration
		} `mapstructure:"http"`
	}
	if err := viper.Unmarshal(&config); err != nil {
		t.Fatal(err)
	}
	if !config.Dry || config.App != "synthetic-consumer" || config.HTTP.Listen != "127.0.0.1:12345" || config.HTTP.Timeout != 2*time.Second {
		t.Fatalf("decode changed: %#v", config)
	}
}

func TestJWTDependencyRejectsOversizedAndInvalidTokens(t *testing.T) {
	parser := jwt.NewParser()
	malformed := strings.Repeat(".", 1<<18)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 10; i++ {
		if _, _, err := parser.ParseUnverified(malformed, jwt.MapClaims{}); err == nil {
			t.Fatal("malformed token accepted")
		}
	}
	runtime.ReadMemStats(&after)
	bytesPerToken := (after.TotalAlloc - before.TotalAlloc) / 10
	if bytesPerToken > 64<<10 {
		t.Fatalf("malformed token allocated %d bytes", bytesPerToken)
	}
	bad := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()})
	signed, err := bad.SignedString([]byte("synthetic-wrong-key"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Parse(signed, func(*jwt.Token) (interface{}, error) { return []byte("synthetic-correct-key"), nil })
	if err == nil || !errors.Is(err, jwt.ErrTokenSignatureInvalid) || errors.Is(err, jwt.ErrTokenExpired) || (token != nil && token.Valid) {
		t.Fatalf("bad signature must be rejected before expiration handling: token=%#v error=%v", token, err)
	}
	t.Logf("JWT v5 fixed behavior: oversized input rejected with %d bytes; invalid signature rejected first", bytesPerToken)
}
