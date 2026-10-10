package dependencysecurity_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	jwt "github.com/golang-jwt/jwt/v4"
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
	if err := utils.Settings.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	if utils.Settings.GetBool("dry") {
		t.Fatal("file bool changed")
	}
	if utils.Settings.GetString("APP") != "synthetic-consumer" {
		t.Fatal("case-insensitive key changed")
	}
	if got := utils.Settings.GetStringSlice("labels"); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("list changed: %v", got)
	}
	if utils.Settings.GetDuration("http.timeout") != 2*time.Second {
		t.Fatal("duration changed")
	}
	flags := pflag.NewFlagSet("synthetic", pflag.ContinueOnError)
	flags.Bool("dry", false, "synthetic flag")
	if err := flags.Parse([]string{"--dry"}); err != nil {
		t.Fatal(err)
	}
	if err := utils.Settings.BindPFlags(flags); err != nil {
		t.Fatal(err)
	}
	if !utils.Settings.GetBool("dry") {
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
	if err := utils.Settings.Unmarshal(&config); err != nil {
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
	validation, ok := err.(*jwt.ValidationError)
	if err == nil || !ok || validation.Errors&jwt.ValidationErrorSignatureInvalid == 0 || validation.Errors&jwt.ValidationErrorExpired != 0 || (token != nil && token.Valid) {
		t.Fatalf("bad signature must be rejected before expiration handling: token=%#v error=%v", token, err)
	}
	t.Logf("JWT v4 fixed behavior: oversized input rejected with %d bytes; invalid signature rejected first", bytesPerToken)
}
