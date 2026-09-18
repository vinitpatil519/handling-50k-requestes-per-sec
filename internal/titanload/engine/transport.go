package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/titanedge/titanedge/internal/buildinfo"
)

type httpRequester struct {
	client *http.Client
	ua     string
}

func newHTTPRequester(cfg Config) *httpRequester {
	tr := &http.Transport{
		Proxy: nil, // never route load through a proxy by accident
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: cfg.Concurrency,
		MaxConnsPerHost:     cfg.Connections,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		DisableCompression:  true,
		DisableKeepAlives:   cfg.DisableKeepAlive,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec // opt-in flag
		ReadBufferSize:      32 << 10,
		WriteBufferSize:     32 << 10,
	}
	tr.Protocols = new(http.Protocols)
	switch cfg.Mode {
	case ModeHTTP2:
		// With HTTP1 disabled, http:// URLs use h2c prior knowledge and
		// https:// URLs negotiate h2 via ALPN.
		tr.Protocols.SetHTTP2(true)
		tr.Protocols.SetUnencryptedHTTP2(true)
	default:
		tr.Protocols.SetHTTP1(true)
	}
	return &httpRequester{
		client: &http.Client{
			Transport: tr,
			// Report redirects as 3xx instead of following them.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		ua: "titanload/" + buildinfo.Version,
	}
}

func (h *httpRequester) Do(ctx context.Context, t *Target) (int, int64, error) {
	var body io.Reader
	if len(t.Body) > 0 {
		body = bytes.NewReader(t.Body)
	}
	req, err := http.NewRequestWithContext(ctx, t.Method, t.URL, body)
	if err != nil {
		return 0, 0, err
	}
	for k, vs := range t.Headers {
		req.Header[k] = vs
	}
	req.Header.Set("User-Agent", h.ua)
	if len(t.Body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		return resp.StatusCode, n, err
	}
	return resp.StatusCode, n, nil
}

func (h *httpRequester) Close() { h.client.CloseIdleConnections() }

// grpcRequester spreads calls over several HTTP/2 connections; a single
// connection is capped by the server's MaxConcurrentStreams and by one TCP
// flow, which would make titanload the bottleneck.
type grpcRequester struct {
	conns   []*grpc.ClientConn
	clients []healthpb.HealthClient
	next    atomic.Uint64
	service string
}

func newGRPCRequester(cfg Config) (*grpcRequester, error) {
	target, useTLS := grpcTarget(cfg.Targets[0].URL)
	creds := insecure.NewCredentials()
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: cfg.Insecure}) //nolint:gosec // opt-in flag
	}
	n := cfg.Connections
	if n <= 0 {
		n = min(max(cfg.Concurrency/100, 4), 64)
	}
	r := &grpcRequester{service: cfg.GRPCService}
	for i := 0; i < n; i++ {
		cc, err := grpc.NewClient(target,
			grpc.WithTransportCredentials(creds),
			grpc.WithUserAgent("titanload/"+buildinfo.Version),
		)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.conns = append(r.conns, cc)
		r.clients = append(r.clients, healthpb.NewHealthClient(cc))
	}
	return r, nil
}

// grpcTarget converts grpc://host:port or grpcs://host:port to a dial target.
func grpcTarget(u string) (string, bool) {
	switch {
	case strings.HasPrefix(u, "grpcs://"):
		return strings.TrimPrefix(u, "grpcs://"), true
	case strings.HasPrefix(u, "grpc://"):
		return strings.TrimPrefix(u, "grpc://"), false
	case strings.HasPrefix(u, "https://"):
		return strings.TrimPrefix(u, "https://"), true
	case strings.HasPrefix(u, "http://"):
		return strings.TrimPrefix(u, "http://"), false
	}
	return u, false
}

func (g *grpcRequester) Do(ctx context.Context, _ *Target) (int, int64, error) {
	c := g.clients[g.next.Add(1)%uint64(len(g.clients))]
	resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{Service: g.service})
	if err != nil {
		return 0, 0, err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return http.StatusServiceUnavailable, 0, nil
	}
	return http.StatusOK, int64(len(resp.String())), nil
}

func (g *grpcRequester) Close() {
	for _, c := range g.conns {
		_ = c.Close()
	}
}

// Classify maps an error to a short, low-cardinality kind for reporting.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
		switch s.Code() {
		case codes.DeadlineExceeded:
			return "timeout"
		case codes.Unavailable:
			return "unavailable"
		case codes.ResourceExhausted:
			return "resource_exhausted"
		default:
			return "grpc_" + strings.ToLower(s.Code().String())
		}
	}
	var (
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		uaErr   x509.UnknownAuthorityError
		recErr  tls.RecordHeaderError
		opErr   *net.OpError
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "conn_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "conn_reset"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "no_ephemeral_ports"
	case errors.Is(err, syscall.EMFILE):
		return "too_many_open_files"
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &certErr) || errors.As(err, &uaErr) || errors.As(err, &recErr):
		return "tls"
	case errors.As(err, &opErr):
		return "net_" + opErr.Op
	}
	return "other"
}
