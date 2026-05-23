// Package render implements the template-driven file renderer.
// Templates are embedded from the templates/ directory via go:embed.
// The renderer is a pure function of its inputs — identical inputs produce
// byte-identical outputs (deterministic rendering).
package render

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed all:templates
var templatesFS embed.FS

// templatesRoot is the path inside the embed.FS where templates live.
const templatesRoot = "templates"

// RenderResult holds a single rendered file.
type RenderResult struct {
	RelPath string // relative path under the project directory
	Content []byte
	Hash    string // SHA-256 hex
}

// RenderContext holds all parameters passed to every template.
type RenderContext struct {
	Tenant TenantCtx
	Site   *SiteCtx // nil for tenant-level renders
	Shared *SharedCtx
	Creds  *CredsCtx
	PKI    *PKICtx
}

// TenantCtx exposes tenant fields to templates.
type TenantCtx struct {
	ID                string
	Name              string
	Domain            string
	Retention         string
	ChunkTimeInterval string
	CompressionOn     bool
	SchemaVersion     int
}

// SiteCtx exposes site fields to templates.
type SiteCtx struct {
	ID                   string
	TenantID             string
	BrokerKind           string
	Throughput           string
	Direction            string
	PayloadCodec         string
	IngestReplicas       int
	BatchSize            int
	FlushIntervalMS      int
	UseSharedSubscription bool
	MQTTTopicFilter      string
	SNIHostname          string // ste_<id>.tnt_<id>.<domain>
	SANs                 []string
	SchemaVersion        int
}

// SharedCtx exposes shared-infrastructure fields.
type SharedCtx struct {
	ACMEEnabled bool
	Domain      string
}

// CredsCtx exposes generated credentials to templates.
type CredsCtx struct {
	SuperuserName string
	SuperuserPass string
	IngestName    string
	IngestPass    string
	EMQXKeyID     string
	EMQXKeySecret string
}

// PKICtx exposes PKI artifact paths.
type PKICtx struct {
	CAPath     string
	CertPath   string
	KeyPath    string
}

// Renderer renders all templates for a given context.
type Renderer struct {
	fs fs.FS
}

// New returns a Renderer using the embedded templates FS.
func New() *Renderer {
	sub, err := fs.Sub(templatesFS, templatesRoot)
	if err != nil {
		panic(fmt.Sprintf("embed sub: %v", err))
	}
	return &Renderer{fs: sub}
}

// RenderShared renders the shared Traefik compose project files.
func (r *Renderer) RenderShared(ctx RenderContext) ([]RenderResult, error) {
	return r.renderDir("shared", ctx)
}

// RenderTenantTSDB renders the per-tenant TimescaleDB compose project files.
func (r *Renderer) RenderTenantTSDB(ctx RenderContext) ([]RenderResult, error) {
	return r.renderDir("tenant/tsdb", ctx)
}

// RenderSite renders the per-site broker + ingest compose files.
// The broker subdirectory is selected based on ctx.Site.BrokerKind.
func (r *Renderer) RenderSite(ctx RenderContext) ([]RenderResult, error) {
	if ctx.Site == nil {
		return nil, fmt.Errorf("RenderSite: Site context is nil")
	}
	var results []RenderResult
	// Broker-specific templates
	brokerDir := "site/" + ctx.Site.BrokerKind
	brokerResults, err := r.renderDir(brokerDir, ctx)
	if err != nil {
		return nil, fmt.Errorf("render broker %s: %w", ctx.Site.BrokerKind, err)
	}
	results = append(results, brokerResults...)
	// Shared ingest template
	ingestResults, err := r.renderDir("site/ingest", ctx)
	if err != nil {
		return nil, fmt.Errorf("render ingest: %w", err)
	}
	results = append(results, ingestResults...)
	return results, nil
}

// RenderTraefikDynamic renders the per-site Traefik dynamic config files.
func (r *Renderer) RenderTraefikDynamic(ctx RenderContext) ([]RenderResult, error) {
	return r.renderDir("shared/traefik/dynamic", ctx)
}

// renderDir walks all *.tmpl files under dir (relative to the embedded FS root)
// and renders each one into a RenderResult.
func (r *Renderer) renderDir(dir string, ctx RenderContext) ([]RenderResult, error) {
	var results []RenderResult
	err := fs.WalkDir(r.fs, dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		raw, err := fs.ReadFile(r.fs, path)
		if err != nil {
			return fmt.Errorf("read template %s: %w", path, err)
		}
		tmpl, err := template.New(path).Funcs(funcMap()).Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", path, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, ctx); err != nil {
			return fmt.Errorf("execute template %s: %w", path, err)
		}
		content := buf.Bytes()
		h := sha256.Sum256(content)
		// relPath strips the .tmpl suffix to produce the output filename.
		relPath := strings.TrimSuffix(strings.TrimPrefix(path, dir+"/"), "")
		if strings.HasSuffix(relPath, ".tmpl") {
			relPath = relPath[:len(relPath)-5]
		}
		results = append(results, RenderResult{
			RelPath: relPath,
			Content: content,
			Hash:    hex.EncodeToString(h[:]),
		})
		return nil
	})
	return results, err
}

// WriteResults writes all RenderResult files under baseDir, creating parent
// directories as needed. Uses atomic-rename for files that already exist.
func WriteResults(baseDir string, results []RenderResult) error {
	for _, r := range results {
		dst := filepath.Join(baseDir, r.RelPath)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		// Write to temp file in same dir then rename for atomicity.
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, r.Content, 0o640); err != nil {
			return fmt.Errorf("write temp %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, dst); err != nil {
			return fmt.Errorf("rename to %s: %w", dst, err)
		}
	}
	return nil
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"indent": func(n int, s string) string {
			pad := strings.Repeat(" ", n)
			return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
		},
		// seq returns a slice []int{0, 1, ..., n-1} for use in range.
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
	}
}
