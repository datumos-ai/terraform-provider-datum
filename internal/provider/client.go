package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the minimal HTTP transport to the Datum API. It speaks to the
// ORGANISATION host with an org-admin API key; the space a call targets is
// always in the path (`/api/spaces/{id}`, `/api/manifest/spaces/{id}`), never
// chosen by the Host header — the same rule the `datum` CLI follows.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	userAgent  string
}

// APIError is a non-2xx answer, with the server's structured body when it
// sent one ({ error, message, details }).
type APIError struct {
	Host    string
	Status  int
	Code    string
	Message string
	Body    string
	// Issues carries the manifest engine's document-path findings when the
	// server sent any ({ path, message }) — rendered one per line.
	Issues []APIIssue
}

// APIIssue is one finding of a refused manifest, addressed by document path.
type APIIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if len(e.Issues) > 0 {
		lines := make([]string, 0, len(e.Issues))
		for _, is := range e.Issues {
			if is.Path != "" {
				lines = append(lines, fmt.Sprintf("  %s: %s", is.Path, is.Message))
			} else {
				lines = append(lines, "  "+is.Message)
			}
		}
		return fmt.Sprintf("datum at %s refused the manifest (%d %s):\n%s", e.Host, e.Status, e.Code, strings.Join(lines, "\n"))
	}
	if e.Message != "" {
		return fmt.Sprintf("datum at %s answered %d %s: %s", e.Host, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("datum at %s answered %d: %s", e.Host, e.Status, strings.TrimSpace(e.Body))
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.Status == http.StatusNotFound
}

// IsManifestInvalid reports whether err is the manifest engine refusing the
// document itself (400 manifest_invalid) — a field the server does not know
// (any more), a value it rejects — as opposed to a transport, auth or
// server failure.
func IsManifestInvalid(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.Status == http.StatusBadRequest && apiErr.Code == "manifest_invalid"
}

// BaseURL is the organisation or space host this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

func NewClient(baseURL, apiKey, userAgent string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// Do performs one JSON call. `out` may be nil when the body is irrelevant.
func (c *Client) Do(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		apiErr := &APIError{Host: c.baseURL, Status: res.StatusCode, Body: string(raw)}
		var parsed struct {
			Error   string     `json:"error"`
			Code    string     `json:"code"`
			Message string     `json:"message"`
			Details any        `json:"details"`
			Issues  []APIIssue `json:"issues"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			// Datum answers { error: "<human message>", code?: "<machine code>" }.
			apiErr.Code = parsed.Code
			apiErr.Message = parsed.Error
			if apiErr.Message == "" {
				apiErr.Message = parsed.Message
			}
			apiErr.Issues = parsed.Issues
			if apiErr.Message == "" && parsed.Details != nil {
				if d, err := json.Marshal(parsed.Details); err == nil {
					apiErr.Message = string(d)
				}
			}
		}
		return apiErr
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: decode body: %w", method, path, err)
		}
	}
	return nil
}

// ---- Spaces -------------------------------------------------------------

// Space is the subset of GET /api/spaces/{id} the provider manages.
type Space struct {
	ID                    string   `json:"id"`
	Slug                  string   `json:"slug"`
	DisplayName           string   `json:"displayName"`
	Status                string   `json:"status"`
	NamespaceCapabilities bool     `json:"namespaceCapabilities"`
	NamespaceMembership   bool     `json:"namespaceMembership"`
	IncidentPublishers    []string `json:"incidentPublishers"`
}

func (c *Client) GetSpace(ctx context.Context, id string) (*Space, error) {
	var out Space
	if err := c.Do(ctx, http.MethodGet, "/api/spaces/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateSpace(ctx context.Context, slug, displayName string) (*Space, error) {
	var out Space
	body := map[string]any{"slug": slug, "displayName": displayName}
	if err := c.Do(ctx, http.MethodPost, "/api/spaces", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchSpace sends PATCH /api/spaces/{id} with the given fields
// (displayName, namespaceCapabilities, namespaceMembership, incidentPublishers…).
func (c *Client) PatchSpace(ctx context.Context, id string, patch map[string]any) (*Space, error) {
	var out Space
	if err := c.Do(ctx, http.MethodPatch, "/api/spaces/"+id, patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetSpaceStatus flips active/suspended (POST /api/spaces/{id}/status).
func (c *Client) SetSpaceStatus(ctx context.Context, id, status string) (*Space, error) {
	var out Space
	if err := c.Do(ctx, http.MethodPost, "/api/spaces/"+id+"/status", map[string]any{"status": status}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- Manifest -----------------------------------------------------------

// ManifestOp is one planned/applied operation reported by the engine.
type ManifestOp struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Op      string `json:"op"`
	Changes any    `json:"changes,omitempty"`
}

// ManifestResult is the answer of PUT /api/manifest/spaces/{id} (or of
// PUT /api/space-manifest, which also names the space it resolved).
type ManifestResult struct {
	SpaceID  string          `json:"spaceId"`
	Applied  bool            `json:"applied"`
	Plan     []ManifestOp    `json:"plan"`
	Observed json.RawMessage `json:"observed"`
	Warnings []string        `json:"warnings,omitempty"`
}

type manifestRequest struct {
	Spec   json.RawMessage `json:"spec"`
	DryRun bool            `json:"dryRun"`
	Prune  bool            `json:"prune"`
}

// SpaceManifestID answers the id of the space the host resolves to
// (GET /api/space-manifest) — the space-admin mode's identity.
func (c *Client) SpaceManifestID(ctx context.Context) (string, error) {
	var out struct {
		SpaceID string `json:"spaceId"`
	}
	if err := c.Do(ctx, http.MethodGet, "/api/space-manifest", nil, &out); err != nil {
		return "", err
	}
	return out.SpaceID, nil
}

// ValidateManifest checks the document without a space: the schema,
// duplicate keys and the references that resolve organisation-wide (nicks,
// system slugs, feature dependencies) — what plan can do before the space
// exists; space-bound references are checked by the dry run.
func (c *Client) ValidateManifest(ctx context.Context, spec json.RawMessage) error {
	return c.Do(ctx, http.MethodPost, "/api/manifest/validate", map[string]any{"spec": spec}, nil)
}

// ApplyManifest sends the desired spec; dryRun=true only plans. The server
// answers `observed` PROJECTED onto the kinds and natural keys the desired
// spec manages, so a clean apply round-trips byte-for-byte after JSON
// normalisation.
//
// An EMPTY spaceID addresses the request-space surface (`/api/space-manifest`):
// the provider points at the space's host with a space-bound key and the
// server resolves the space from the host — the space admin's mode.
func (c *Client) ApplyManifest(ctx context.Context, spaceID string, spec json.RawMessage, dryRun, prune bool) (*ManifestResult, error) {
	var out ManifestResult
	req := manifestRequest{Spec: spec, DryRun: dryRun, Prune: prune}
	path := "/api/space-manifest"
	if spaceID != "" {
		path = "/api/manifest/spaces/" + spaceID
	}
	if err := c.Do(ctx, http.MethodPut, path, req, &out); err != nil {
		return nil, err
	}
	if out.SpaceID == "" {
		out.SpaceID = spaceID
	}
	return &out, nil
}
