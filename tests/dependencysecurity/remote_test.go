package dependencysecurity_test

import (
	"bytes"
	"encoding/binary"
	consul "github.com/hashicorp/consul/api"
	"github.com/pkg/sftp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProtoJSONRejectsMissingValue(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- protojson.Unmarshal([]byte("{\"\":}"), &anypb.Any{}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid JSON accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("invalid JSON did not terminate")
	}
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }
func TestSFTPPacketLengthIsBounded(t *testing.T) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], 16<<20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	client, err := sftp.NewClientPipe(bytes.NewReader(prefix[:]), discardWriteCloser{})
	runtime.ReadMemStats(&after)
	if client != nil {
		client.Close()
	}
	if err == nil {
		t.Fatal("truncated oversized packet accepted")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Fatalf("oversized packet allocated %d bytes", n)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConsulClientSetsContentType(t *testing.T) {
	seen := ""
	config := consul.DefaultConfig()
	config.Address = "127.0.0.1:1"
	config.HttpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get("Content-Type")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("null"))}, nil
	})}
	client, err := consul.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.KV().Get("synthetic", nil); err != nil {
		t.Fatal(err)
	}
	if seen != "application/json" {
		t.Fatalf("API request lacks expected content type: %q", seen)
	}
}
