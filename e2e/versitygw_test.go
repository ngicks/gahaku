package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	gahakuv1 "github.com/ngicks/gahaku/api/gen/proto/go/ngicks/gahaku/v1"
)

// What versitygw is started with, read off the image itself rather than from
// memory: its entrypoint execs the arguments the container is given as
// versitygw's own, `versitygw --help` names ROOT_ACCESS_KEY / ROOT_SECRET_KEY
// as the root credential variables, and the posix backend takes its top-level
// directory as a positional argument.
const (
	versitygwImage = "docker.io/versity/versitygw:latest"
	// versitygwPort is the gateway's default S3 port inside the container.
	versitygwPort      = "7070"
	versitygwAccessKey = "gahakue2e"
	versitygwSecretKey = "gahakue2esecret"
	// versitygwRegion is the gateway's default. A presign signed for another
	// region comes back as a 403 that reads like an expired url.
	versitygwRegion = "us-east-1"
)

const (
	// pullTimeout is generous: the image is pulled cold on a first run.
	pullTimeout = 10 * time.Minute
	// versitygwStartTimeout bounds the wait for the gateway to answer.
	versitygwStartTimeout = 60 * time.Second
	// presignExpiry has to outlive the queue wait plus the render, which is
	// what a deployment is told to presign for as well.
	presignExpiry = 10 * time.Minute
)

// endpointEnv points the test at a gateway that is already listening with the
// credentials above, for environments where podman can pull images but not run
// them — this development container is one: it has no /dev/net/tun and a
// read-only cgroup tree, so every `podman run` fails before the entrypoint.
const endpointEnv = "GAHAKU_TEST_VERSITYGW_ENDPOINT"

// versitygwEndpoint returns the S3 endpoint to presign against, starting a
// container for it unless the environment named one.
func versitygwEndpoint(t *testing.T) string {
	t.Helper()
	if endpoint := os.Getenv(endpointEnv); endpoint != "" {
		t.Logf("presigning against the versitygw at %s (%s)", endpoint, endpointEnv)
		return endpoint
	}
	return runVersitygw(t)
}

func runVersitygw(t *testing.T) string {
	t.Helper()
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skipf("podman is not installed and %s is unset: %v", endpointEnv, err)
	}

	pullCtx, cancel := context.WithTimeout(t.Context(), pullTimeout)
	defer cancel()
	pull := exec.CommandContext(pullCtx, podman, "pull", versitygwImage)
	if out, err := pull.CombinedOutput(); err != nil {
		t.Skipf("pulling %s failed, likely without network: %v\n%s", versitygwImage, err, out)
	}

	addr := freeAddr(t)
	name := fmt.Sprintf("gahaku-e2e-versitygw-%d", time.Now().UnixNano())
	run := exec.CommandContext(t.Context(), podman, "run",
		"--detach",
		"--rm",
		"--name", name,
		"--publish", addr+":"+versitygwPort,
		"--env", "ROOT_ACCESS_KEY="+versitygwAccessKey,
		"--env", "ROOT_SECRET_KEY="+versitygwSecretKey,
		"--volume", t.TempDir()+":/data",
		versitygwImage,
		"posix", "/data",
	)
	if out, err := run.CombinedOutput(); err != nil {
		t.Skipf("podman cannot run containers here: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(podman, "rm", "--force", name).CombinedOutput(); err != nil {
			t.Logf("removing the versitygw container: %v\n%s", err, out)
		}
	})

	endpoint := "http://" + addr
	waitVersitygw(t, podman, name, endpoint)
	return endpoint
}

// waitVersitygw polls the gateway until it answers at all: an unsigned request
// is refused with 403, which is as much of a readiness signal as a 200 would be.
func waitVersitygw(t *testing.T, podman, name, endpoint string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(versitygwStartTimeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := exec.Command(podman, "logs", name).CombinedOutput()
	t.Fatalf("versitygw did not answer at %s within %s\n%s",
		endpoint, versitygwStartTimeout, logs)
}

func versitygwClient(endpoint string) *s3.Client {
	return s3.New(s3.Options{
		Region:       versitygwRegion,
		BaseEndpoint: aws.String(endpoint),
		// The gateway serves path-style addressing; virtual-host style would
		// put the bucket in a hostname nothing resolves.
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			versitygwAccessKey, versitygwSecretKey, "",
		),
		// Keeps the uploads of this test to the plain PUT the gateway is sure
		// to accept, rather than the trailing checksums newer SDKs add by
		// default. Presigning already drops them by itself.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	})
}

// The whole presigned round trip against a real S3 implementation: the caller
// presigns, the server holds no credentials and does nothing but GET the source
// url and PUT the pages to theirs. What the httptest store of presign_test.go
// cannot show is exactly what is under test here — that the urls gahaku's plain
// net/http requests carry still verify when a real SigV4 implementation checks
// them.
func TestRenderRoundTripsThroughVersitygw(t *testing.T) {
	requireBinary(t)
	ctx := t.Context()

	endpoint := versitygwEndpoint(t)
	store := versitygwClient(endpoint)
	bucket := fmt.Sprintf("gahaku-e2e-%d", time.Now().UnixNano())
	if _, err := store.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("creating the bucket: %v", err)
	}

	pages := pdfFixture[:2]
	if _, err := store.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("input.pdf"),
		Body:   bytes.NewReader(buildPDF(pages)),
	}); err != nil {
		t.Fatalf("uploading the input document: %v", err)
	}

	presigner := s3.NewPresignClient(store, s3.WithPresignExpires(presignExpiry))
	source, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("input.pdf"),
	})
	if err != nil {
		t.Fatalf("presigning the source url: %v", err)
	}
	destinations := make([]string, 0, len(pages))
	for i := range pages {
		put, err := presigner.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(fmt.Sprintf("page-%d.png", i)),
		})
		if err != nil {
			t.Fatalf("presigning the destination of page %d: %v", i, err)
		}
		destinations = append(destinations, put.URL)
	}

	client := startUrlServer(t).client(t)
	header := urlSource(putOutput(byteStreamHeader(), destinations...), source.URL)
	header.Options = &gahakuv1.RenderOptions{
		Dpi:   72,
		Pages: &gahakuv1.PageRange{FirstPage: 1, LastPage: uint32(len(pages))},
	}
	responses, err := exchange(t, client.RenderPdf, headerRequest(header))
	if err != nil {
		t.Fatalf("RenderPdf: %v", err)
	}

	uploaded := uploadedPages(t, responses)
	if len(uploaded) != len(pages) {
		t.Fatalf("want a completion per page, got %d", len(uploaded))
	}
	for i, page := range pages {
		key := fmt.Sprintf("page-%d.png", i)
		object, err := store.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			t.Fatalf("downloading %s: %v", key, err)
		}
		body, err := io.ReadAll(object.Body)
		_ = object.Body.Close()
		if err != nil {
			t.Fatalf("reading %s: %v", key, err)
		}
		if uint64(len(body)) != uploaded[i].GetSizeBytes() {
			t.Errorf("%s is %d bytes, the render reported %d",
				key, len(body), uploaded[i].GetSizeBytes())
		}

		decoded := decodePage(t, body)
		if decoded.format != "png" {
			t.Errorf("%s is a %s, want the default png", key, decoded.format)
		}
		// At 72 dpi a page is as many pixels as it is points wide, so the width
		// says which document page landed at this key.
		assertPixels(t, key+" width", decoded.config.Width, page.width, 72)
	}
}
