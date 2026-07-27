// Package quadlet exercises deployment/quadlet/ the way an operator runs it:
// under a systemd that generates the units and starts what they describe.
//
// A development host generally has neither systemd nor a machine to spare, so
// the units run inside a privileged container that has systemd as PID 1 and a
// podman of its own (D-E). That container is rootful, which is the one
// deliberate deviation from deployment/quadlet/README.md — the units install
// rootless there; see unitDir in podman_test.go.
//
// The suite is opt-in. It pulls hundreds of megabytes of images and needs a
// podman that can run a privileged container, which is more than an ordinary
// `go test ./...` should require, so every test skips unless
// GAHAKU_TEST_QUADLET is set — the switch `make test-quadlet` throws, and the
// deployment counterpart of e2e/'s GAHAKU_TEST_E2E.
package quadlet

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// enableEnv opts the suite in.
const enableEnv = "GAHAKU_TEST_QUADLET"

// What the units are driven with. The keys are what the harness writes into
// the EnvironmentFile versitygw.container reads; the port and region are the
// gateway's defaults, which deployment/quadlet/versitygw.container leaves
// alone.
const (
	gatewayPort = "7070"
	accessKey   = "gahakuquadlet"
	secretKey   = "gahakuquadletsecret"
	region      = "us-east-1"
)

const (
	// gatewayTimeout bounds the wait for the gateway to answer once its unit
	// is active.
	gatewayTimeout = 60 * time.Second
	// presignExpiry matches what a deployment is told to presign with.
	presignExpiry = 10 * time.Minute
)

// generated is every service the four units must produce. The two supporting
// units have no [Install] of their own — they are reached through the
// Requires= the generator derives from Network=/Volume=, which is exactly what
// asserting on them here checks.
var generated = []string{
	"gahaku-network.service",
	"versitygw-volume.service",
	"versitygw.service",
	"gahaku.service",
}

func requireQuadlet(t *testing.T) {
	t.Helper()
	if os.Getenv(enableEnv) == "" {
		t.Skipf("set %s=1 to run the quadlet deployment harness", enableEnv)
	}
}

// TestQuadletStackConverges is the deployment test PLAN.md describes: the
// units generate, versitygw.service comes up, and an object survives a round
// trip through urls the gateway itself signed.
//
// gahaku.service is asserted to exist rather than to run. Its image is built
// from the repository's Dockerfile and published nowhere, so making it start
// here would mean building LibreOffice into an image inside the throwaway
// container, or streaming a host-built one in — minutes of work per run for a
// container the presigned round trip does not touch. What the harness owes the
// unit is that the generator accepts it; that its image is absent is reported,
// not failed.
func TestQuadletStackConverges(t *testing.T) {
	requireQuadlet(t)
	s := start(t)

	// The operator's own step: units are copied in, then daemon-reload runs
	// the generator over them. Boot already did it once, so this asserts the
	// reload path rather than the boot path.
	if out, err := s.exec(t.Context(), "systemctl", "daemon-reload"); err != nil {
		t.Fatalf("daemon-reload: %v\n%s", err, out)
	}

	for _, unit := range generated {
		if out, err := s.exec(t.Context(), "systemctl", "cat", unit); err != nil {
			t.Errorf("the generator produced no %s: %v\n%s", unit, err, out)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	s.waitActive(t, "versitygw.service")
	waitGateway(t, s)

	roundTrip(t, s.endpoint)

	state, _ := s.exec(t.Context(), "systemctl", "is-active", "gahaku.service")
	t.Logf("gahaku.service is %q; its localhost/gahaku:latest image is not "+
		"built inside this container, so anything but active is expected here", state)
}

// waitGateway polls until the gateway answers at all: an unsigned request is
// refused with 403, which is as much of a readiness signal as a 200 would be.
func waitGateway(t *testing.T, s *systemd) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(gatewayTimeout); time.Now().Before(deadline); {
		resp, err := client.Get(s.endpoint)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("the gateway did not answer at %s within %s\n%s",
		s.endpoint, gatewayTimeout, s.diagnose(t))
}

// gatewayClient is a second copy of what e2e/versitygw_test.go builds rather
// than a shared helper: test packages cannot import one another's identifiers,
// and the alternative is exporting an S3 client factory from non-test code for
// nobody's benefit.
func gatewayClient(endpoint string) *s3.Client {
	return s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		// The gateway serves path-style addressing; virtual-host style would
		// put the bucket in a hostname nothing resolves.
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			accessKey, secretKey, "",
		),
		// Keeps the uploads of this test to the plain PUT the gateway is sure
		// to accept, rather than the trailing checksums newer SDKs add by
		// default. Presigning already drops them by itself.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	})
}

// roundTrip is the presigned exchange gahaku performs at render time, done by
// the test itself: a GET of a source object through a signed url, and a PUT of
// a rendered one through another. Nothing but net/http touches those urls, the
// way nothing but net/http does in pkg/input and pkg/output.
func roundTrip(t *testing.T, endpoint string) {
	t.Helper()
	ctx := t.Context()
	store := gatewayClient(endpoint)

	bucket := fmt.Sprintf("gahaku-quadlet-%d", time.Now().UnixNano())
	if _, err := store.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}

	source := []byte("the document a caller would have gahaku render")
	if _, err := store.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("source"),
		Body:   bytes.NewReader(source),
	}); err != nil {
		t.Fatalf("uploading the source object: %v", err)
	}

	presigner := s3.NewPresignClient(store, s3.WithPresignExpires(presignExpiry))
	get, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("source"),
	})
	if err != nil {
		t.Fatalf("presigning the source url: %v", err)
	}
	if fetched := do(t, http.MethodGet, get.URL, nil); !bytes.Equal(fetched, source) {
		t.Errorf("the presigned GET returned %q, want %q", fetched, source)
	}

	rendered := []byte("the page gahaku would have uploaded")
	put, err := presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("rendered"),
	})
	if err != nil {
		t.Fatalf("presigning the destination url: %v", err)
	}
	do(t, http.MethodPut, put.URL, rendered)

	object, err := store.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("rendered"),
	})
	if err != nil {
		t.Fatalf("downloading what the presigned PUT stored: %v", err)
	}
	stored, err := io.ReadAll(object.Body)
	_ = object.Body.Close()
	if err != nil {
		t.Fatalf("reading what the presigned PUT stored: %v", err)
	}
	if !bytes.Equal(stored, rendered) {
		t.Errorf("the presigned PUT stored %q, want %q", stored, rendered)
	}
}

// do performs one plain request against a presigned url and fails on anything
// but a 2xx. No Content-Type is set: the presign did not sign one, and a
// header the signature does not cover is a 403 here.
func do(t *testing.T, method, url string, body []byte) []byte {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatalf("building the %s request: %v", method, err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s against the presigned url: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the %s response: %v", method, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf("%s against the presigned url answered %s\n%s", method, resp.Status, answer)
	}
	return answer
}
