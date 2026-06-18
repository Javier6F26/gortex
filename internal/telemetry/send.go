package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

// sendSchemaVersion is the wire-contract version of the aggregate payload.
// Bump it (and specs/telemetry-contract.md) on any payload-shape change so the
// ingest endpoint can branch on it.
const sendSchemaVersion = 1

// EnvEndpoint names the environment variable that configures the ingest
// endpoint URL. There is deliberately NO built-in default: when it is empty,
// telemetry is aggregated locally but NEVER transmitted. The live-send path is
// gated on an operator setting this to a confirmed ingest URL — until then the
// whole pipeline runs end to end except the final POST, which is skipped.
const EnvEndpoint = "GORTEX_TELEMETRY_ENDPOINT"

const (
	installIDFile = "install-id"
	lastSendFile  = "last-send"
	sendTimeout   = 5 * time.Second
)

// InstallID returns a stable anonymous per-machine id, minting and persisting a
// random UUIDv4 on first use. It is the only stable identifier telemetry
// carries — it ties a machine's daily aggregates together without revealing who
// or where. Fail-soft: if dir is unwritable the id is returned for this process
// but not persisted, so a payload stays well-formed and the next run re-mints.
func InstallID(dir string) string {
	path := filepath.Join(dir, installIDFile)
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	id := uuid.NewString()
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(path, []byte(id+"\n"), 0o644)
	}
	return id
}

// Payload is the daily-aggregate envelope POSTed to the ingest endpoint. Every
// field is coarse and non-identifying — an anonymous install id, the platform,
// and bucketed daily counts. See specs/telemetry-contract.md for the contract.
type Payload struct {
	InstallID     string    `json:"install_id"`
	SchemaVersion int       `json:"schema_version"`
	GortexVersion string    `json:"gortex_version"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	CI            bool      `json:"ci"`
	Days          []*Rollup `json:"days"`
}

// BuildPayload assembles the envelope from completed-day rollups.
func BuildPayload(installID, version string, days []*Rollup, getenv func(string) string) Payload {
	if getenv == nil {
		getenv = os.Getenv
	}
	return Payload{
		InstallID:     installID,
		SchemaVersion: sendSchemaVersion,
		GortexVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		CI:            isCI(getenv),
		Days:          days,
	}
}

// isCI reports whether this looks like a CI environment, so aggregates can be
// separated from interactive use. Honors the de-facto `CI` variable.
func isCI(getenv func(string) string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv("CI")))
	return v != "" && v != "0" && v != "false"
}

// Sender transmits completed daily rollups to the ingest endpoint, once per UTC
// day at most. It is fail-silent and a no-op whenever the endpoint is empty
// (the blocked state) or consent is disabled.
type Sender struct {
	store    *Store
	dir      string
	endpoint string
	version  string
	client   *http.Client
	now      func() time.Time
	getenv   func(string) string
}

// NewSender builds a sender. The endpoint is read from EnvEndpoint with no
// fallback — an empty result leaves live send disabled.
func NewSender(store *Store, dir, version string, getenv func(string) string) *Sender {
	if getenv == nil {
		getenv = os.Getenv
	}
	return &Sender{
		store:    store,
		dir:      dir,
		endpoint: strings.TrimSpace(getenv(EnvEndpoint)),
		version:  version,
		client:   &http.Client{Timeout: sendTimeout},
		now:      time.Now,
		getenv:   getenv,
	}
}

// MaybeSend opportunistically transmits completed days. It no-ops unless every
// condition holds: consent enabled, an endpoint configured, this machine has
// not already attempted a send today, and there is at least one completed day
// buffered. It never returns an error — a failed send leaves the days buffered
// for the next day's attempt.
func (s *Sender) MaybeSend(ctx context.Context, consent Consent) {
	if s == nil || !consent.Enabled || s.endpoint == "" {
		return
	}
	today := DayKey(s.now())
	if s.lastSendDay() == today {
		return
	}
	days, err := s.store.LoadCompleted(today)
	if err != nil || len(days) == 0 {
		return
	}
	// Mark the attempt before sending so a slow or failing endpoint cannot
	// produce more than one attempt per day; unsent days remain buffered and
	// ship tomorrow.
	s.markSent(today)

	payload := BuildPayload(InstallID(s.dir), s.version, days, s.getenv)
	if err := s.post(ctx, payload); err != nil {
		return // fail-silent
	}
	for _, d := range days {
		_ = s.store.Delete(d.Day)
	}
}

// post POSTs the payload as JSON. Any non-2xx response is an error; there are
// no retries — a single attempt per day is the contract.
func (s *Sender) post(ctx context.Context, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telemetry ingest returned status %d", resp.StatusCode)
	}
	return nil
}

func (s *Sender) lastSendDay() string {
	b, err := os.ReadFile(filepath.Join(s.dir, lastSendFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (s *Sender) markSent(day string) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.dir, lastSendFile), []byte(day+"\n"), 0o644)
}
