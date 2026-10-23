package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/trust"
)

// Under -local-tls a preview's front door is signed by AppTown's own
// authority. The authority joins the deployments use, and only it, so the
// front-page check (front-page-answers) reaches a preview served the way
// apptownd serves it and judges it: the desktop trial's first build was
// blocked on "certificate signed by unknown authority" there.
//
// Not parallel: it installs the process's trust.
func TestTheFrontPageCheckReachesALocalTLSPreview(t *testing.T) {
	data := t.TempDir()
	wrap, chats, err := localTLS(true, "https://localhost:7443", ":7777", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
	go func() { _ = srv.Serve(wrap(l)) }()
	t.Cleanup(func() { _ = srv.Close() })
	front := "https://localhost:" + strings.TrimPrefix(l.Addr().String(), "127.0.0.1:")

	set, err := loadTrust(data, map[trust.Use]string{}, chats)
	if err != nil {
		t.Fatal(err)
	}
	// A use with no bundle has no pool of its own: the system's roots alone.
	if set.Pool(trust.Deployments) == nil || set.Pool(trust.Inference) != nil || set.Pool(trust.AppTown) != nil {
		t.Fatalf("the local authority belongs to the deployments use alone: deployments %v, inference %v, apptown %v",
			set.Pool(trust.Deployments) != nil, set.Pool(trust.Inference) != nil, set.Pool(trust.AppTown) != nil)
	}
	trust.Install(set)
	t.Cleanup(func() { trust.Install(nil) })
	if state, detail := check.JudgePage(context.Background(), front); state != check.Conforms {
		t.Fatalf("the front-page check on a local-TLS preview: %v %s", state, detail)
	}
}
