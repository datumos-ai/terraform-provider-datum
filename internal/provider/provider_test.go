package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The transport contract with the Datum API: the auth header, the two
// manifest surfaces, and how a refused manifest reads back to the user.
func TestClientApplyManifestUsesTheRightSurfaceAndAuth(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody manifestRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"spaceId": "sp1", "applied": !gotBody.DryRun, "plan": []any{}, "observed": map[string]any{"apiVersion": "datum/v1"},
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL+"/", "k3y", "test")

	res, err := c.ApplyManifest(context.Background(), "sp1", json.RawMessage(`{"apiVersion":"datum/v1"}`), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/manifest/spaces/sp1" || gotAuth != "Bearer k3y" || !gotBody.DryRun {
		t.Fatalf("org-admin mode: path=%q auth=%q dryRun=%v", gotPath, gotAuth, gotBody.DryRun)
	}
	if res.Applied || res.SpaceID != "sp1" {
		t.Fatalf("unexpected result %+v", res)
	}

	// An empty space id addresses the request-space surface (space-admin mode).
	if _, err := c.ApplyManifest(context.Background(), "", json.RawMessage(`{}`), false, true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/space-manifest" || !gotBody.Prune {
		t.Fatalf("space-admin mode: path=%q prune=%v", gotPath, gotBody.Prune)
	}
}

func TestAPIErrorRendersIssuesByPathWithTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown directory user \"ghost\"","code":"manifest_invalid","issues":[{"path":"members[0].user","message":"unknown directory user \"ghost\""},{"path":"","message":"nothing to update"}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "test")
	_, err := c.ApplyManifest(context.Background(), "sp1", json.RawMessage(`{}`), true, false)
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 400 || apiErr.Code != "manifest_invalid" || len(apiErr.Issues) != 2 {
		t.Fatalf("unexpected error %#v", err)
	}
	msg := err.Error()
	for _, want := range []string{srv.URL, "400 manifest_invalid", "  members[0].user: unknown directory user", "  nothing to update"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
	if IsNotFound(err) {
		t.Fatal("400 is not a 404")
	}
	// A plain { error } body (no issues) keeps the host and the message.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown space"}`))
	}))
	defer srv2.Close()
	_, err = NewClient(srv2.URL, "k", "test").GetSpace(context.Background(), "nope")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "unknown space") || !strings.Contains(err.Error(), srv2.URL) {
		t.Fatalf("unexpected 404 rendering: %v", err)
	}
}

func TestPlanHelpers(t *testing.T) {
	ops := []ManifestOp{
		{Kind: "members", Name: "ann", Op: "noop"},
		{Kind: "members", Name: "bob", Op: "create"},
		{Kind: "routines", Name: "Memory", Op: "orphan"},
		{Kind: "features", Name: "chat", Op: "update"},
	}
	if !hasChanges(ops) {
		t.Fatal("create/update are changes")
	}
	if hasChanges([]ManifestOp{{Op: "noop"}, {Op: "orphan"}}) {
		t.Fatal("noop and orphan are not changes")
	}
	if got := summarizePlan(ops); got != "create members/bob, orphan routines/Memory, update features/chat" {
		t.Fatalf("summary %q", got)
	}
	if got := orphansOf(ops); len(got) != 1 || got[0] != "routines/Memory" {
		t.Fatalf("orphans %v", got)
	}
	if !jsonEqual(json.RawMessage(`{"a":1,"b":[1,2]}`), json.RawMessage(`{"b":[1,2],"a":1}`)) {
		t.Fatal("key order must not matter")
	}
	if jsonEqual(json.RawMessage(`{"a":[1,2]}`), json.RawMessage(`{"a":[2,1]}`)) {
		t.Fatal("array order matters")
	}
}

// A recorded spec the server's schema outgrew reads as manifest_invalid —
// Read keeps the state on it instead of blocking the plan; any other
// failure still stops it.
func TestIsManifestInvalidOnlyForTheEnginesRefusal(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 400, Code: "manifest_invalid"}, true},
		{&APIError{Status: 400, Code: "bad_request"}, false},
		{&APIError{Status: 401, Code: "manifest_invalid"}, false},
		{&APIError{Status: 500}, false},
		{&APIError{Status: 404}, false},
		{errors.New("dial tcp: refused"), false},
	}
	for i, c := range cases {
		if got := IsManifestInvalid(c.err); got != c.want {
			t.Errorf("case %d: IsManifestInvalid(%v) = %v, want %v", i, c.err, got, c.want)
		}
	}
}
