//go:build integration

// Integration tests for control-plane (DB) mode. They boot the server through
// the production wiring (router.InitDependencies) against a real PostgreSQL
// and drive it over HTTP, covering what unit tests cannot: schema migrations,
// the postgres stores, and the full publish → serve → rollback lifecycle.
//
// Run with:  make test_integration
// (starts the docker-compose postgres and sets TEST_DB_URL; without
// TEST_DB_URL these tests skip.)
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"expo-open-ota/internal/bucket"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/cdn"
	infrastructure "expo-open-ota/internal/router"
	"expo-open-ota/internal/types"

	"github.com/jackc/pgx/v5"
)

// 32 bytes, base64 — fixed test master key (never a real secret).
const testMasterKeyB64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

type stack struct {
	server   *httptest.Server
	adminJWT string
	appId    string
	apiKey   string
}

// newStack boots a fresh control-plane instance: empty schema, production
// wiring, HTTP server, one app and one API key created via the dashboard API.
// Booting on an EMPTY database with no legacy env config is itself the
// regression test for the fresh-install boot bug.
func newStack(t *testing.T) *stack {
	t.Helper()
	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set — run 'make test_integration' (starts the compose postgres)")
	}

	// Fresh schema per test: migrations and the infra-import run from zero.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting to TEST_DB_URL: %v", err)
	}
	if _, err := conn.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	_ = conn.Close(ctx)

	t.Setenv("DB_URL", dbURL)
	t.Setenv("CONTROL_PLANE_MASTER_KEY_B64", testMasterKeyB64)
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_BUCKET_BASE_PATH", t.TempDir())
	t.Setenv("JWT_SECRET", "integration-secret")
	t.Setenv("ADMIN_PASSWORD", "admin")
	t.Setenv("USE_DASHBOARD", "true")
	t.Setenv("EXPO_APPS_JSON", "")
	t.Setenv("EXPO_APP_ID", "")

	// Singletons capture env on first use; reset so each test's env applies.
	bucket.ResetBucketInstance()
	cdn.ResetCDNInstance()
	_ = cache.GetCache().Clear()

	container, cleanup := infrastructure.InitDependencies(ctx)
	t.Cleanup(cleanup)
	server := httptest.NewServer(infrastructure.NewRouter(container))
	t.Cleanup(server.Close)
	t.Setenv("BASE_URL", server.URL)

	s := &stack{server: server}
	s.adminJWT = s.login(t, "admin")
	s.appId = s.createApp(t, "integration-app")
	s.apiKey = s.createApiKey(t, "integration-key")
	return s
}

func (s *stack) login(t *testing.T, password string) string {
	t.Helper()
	resp, err := http.PostForm(s.server.URL+"/auth/login", url.Values{"password": {password}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	return body.Token
}

func (s *stack) adminJSON(t *testing.T, method, path string, payload string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, s.server.URL+path, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+s.adminJWT)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func (s *stack) createApp(t *testing.T, name string) string {
	t.Helper()
	resp, raw := s.adminJSON(t, "POST", "/api/apps", fmt.Sprintf(`{"name":%q,"keysConfig":{"mode":"database"}}`, name))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create app: %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		AppId string `json:"appId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.AppId == "" {
		t.Fatalf("create app: bad response %s", raw)
	}
	return body.AppId
}

func (s *stack) createApiKey(t *testing.T, name string) string {
	t.Helper()
	resp, raw := s.adminJSON(t, "POST", "/api/apps/"+s.appId+"/apiKeys", fmt.Sprintf(`{"name":%q}`, name))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		ApiKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.ApiKey == "" {
		t.Fatalf("create api key: bad response %s", raw)
	}
	return body.ApiKey
}

func (s *stack) cliRequest(t *testing.T, method, path, apiKey string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, s.server.URL+path, body)
	req.Header.Set("Use-Cli-Auth", "true")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (s *stack) deviceManifest(t *testing.T, channel, runtimeVersion string, extraHeaders map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", s.server.URL+"/manifest", nil)
	req.Header.Set("expo-app-id", s.appId)
	req.Header.Set("expo-channel-name", channel)
	req.Header.Set("expo-platform", "ios")
	req.Header.Set("expo-runtime-version", runtimeVersion)
	req.Header.Set("expo-protocol-version", "1")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("manifest poll: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestFreshInstallBootsAndServesHealth(t *testing.T) {
	s := newStack(t)
	resp, err := http.Get(s.server.URL + "/hc")
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /hc on a fresh install, got %d", resp.StatusCode)
	}
}

func TestApiKeyLifecycle(t *testing.T) {
	s := newStack(t)
	publishPath := "/" + s.appId + "/requestUploadUrl/production?platform=ios&runtimeVersion=1"

	t.Run("valid key authenticates", func(t *testing.T) {
		resp := s.cliRequest(t, "POST", publishPath, s.apiKey, strings.NewReader(`{"fileNames":[]}`), "application/json")
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("valid API key was rejected")
		}
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		resp := s.cliRequest(t, "POST", publishPath, "eoo_wrong", nil, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for a wrong API key, got %d", resp.StatusCode)
		}
	})

	t.Run("revoked key is rejected", func(t *testing.T) {
		_, raw := s.adminJSON(t, "GET", "/api/apps/"+s.appId+"/apiKeys", "")
		var keys []struct {
			Id string `json:"id"`
		}
		if err := json.Unmarshal(raw, &keys); err != nil || len(keys) == 0 {
			t.Fatalf("listing api keys: %s", raw)
		}
		resp, raw := s.adminJSON(t, "DELETE", fmt.Sprintf("/api/apps/%s/apiKeys/%s/revoke", s.appId, keys[0].Id), "")
		if resp.StatusCode >= 300 {
			t.Fatalf("revoking key: %d: %s", resp.StatusCode, raw)
		}
		after := s.cliRequest(t, "POST", publishPath, s.apiKey, nil, "")
		defer after.Body.Close()
		if after.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 after revocation, got %d", after.StatusCode)
		}
	})
}

func TestManifestUnmappedChannelReturns404(t *testing.T) {
	s := newStack(t)
	resp, body := s.deviceManifest(t, "never-mapped", "1", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unmapped channel, got %d: %s", resp.StatusCode, body)
	}
}

// TestPublishServeRollbackLifecycle drives the complete OTA lifecycle over
// HTTP, exactly as eoas + a device would: request upload URLs, upload every
// file of a real update fixture, mark it uploaded, map a channel, poll the
// manifest, download the launch asset, roll back, and observe the directive.
func TestPublishServeRollbackLifecycle(t *testing.T) {
	s := newStack(t)
	fixture, err := filepath.Abs(filepath.Join("..", "test-updates", "test-app-id", "branch-1", "1", "1674170951"))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Request upload URLs for the fixture's files.
	fileNames := fixtureFileNames(t, fixture)
	payload, _ := json.Marshal(map[string]interface{}{"fileNames": fileNames})
	resp := s.cliRequest(t, "POST", "/"+s.appId+"/requestUploadUrl/production?platform=ios&runtimeVersion=1&commitHash=integration", s.apiKey, bytes.NewReader(payload), "application/json")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("requestUploadUrl: %d: %s", resp.StatusCode, raw)
	}
	var uploadPlan struct {
		UpdateId       int64 `json:"updateId"`
		UploadRequests []struct {
			RequestUploadUrl string `json:"requestUploadUrl"`
			FileName         string `json:"fileName"`
			FilePath         string `json:"filePath"`
		} `json:"uploadRequests"`
	}
	if err := json.Unmarshal(raw, &uploadPlan); err != nil {
		t.Fatalf("decoding upload plan: %v: %s", err, raw)
	}
	if len(uploadPlan.UploadRequests) == 0 {
		t.Fatal("expected upload requests, got none")
	}

	// 2. Upload every file (multipart PUT to the returned URLs).
	for _, u := range uploadPlan.UploadRequests {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		f, err := os.Open(filepath.Join(fixture, u.FilePath))
		if err != nil {
			t.Fatalf("opening fixture file %s: %v", u.FilePath, err)
		}
		part, _ := writer.CreateFormFile(u.FileName, u.FileName)
		_, _ = io.Copy(part, f)
		f.Close()
		writer.Close()
		resp := s.cliRequest(t, "PUT", strings.TrimPrefix(u.RequestUploadUrl, s.server.URL), s.apiKey, body, writer.FormDataContentType())
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("uploading %s: %d: %s", u.FileName, resp.StatusCode, msg)
		}
	}

	// 3. Mark the update as uploaded.
	markPath := fmt.Sprintf("/%s/markUpdateAsUploaded/production?platform=ios&runtimeVersion=1&updateId=%d", s.appId, uploadPlan.UpdateId)
	resp = s.cliRequest(t, "POST", markPath, s.apiKey, nil, "")
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("markUpdateAsUploaded: %d: %s", resp.StatusCode, msg)
	}

	// 4. Create the channel mapped to the branch.
	cResp, cRaw := s.adminJSON(t, "POST", "/api/apps/"+s.appId+"/channels", `{"channelName":"production","branchName":"production"}`)
	if cResp.StatusCode >= 300 {
		t.Fatalf("creating channel: %d: %s", cResp.StatusCode, cRaw)
	}

	// 5. Device poll → manifest with the published update.
	mResp, mBody := s.deviceManifest(t, "production", "1", nil)
	if mResp.StatusCode != http.StatusOK {
		t.Fatalf("manifest: expected 200, got %d: %s", mResp.StatusCode, mBody)
	}
	if !strings.Contains(string(mBody), "launchAsset") {
		t.Fatalf("manifest does not contain a launchAsset: %s", mBody)
	}

	// 6. Download the launch asset with device headers.
	assetURL := extractLaunchAssetURL(t, string(mBody))
	req, _ := http.NewRequest("GET", assetURL, nil)
	req.Header.Set("expo-app-id", s.appId)
	req.Header.Set("expo-channel-name", "production")
	req.Header.Set("expo-platform", "ios")
	req.Header.Set("expo-runtime-version", "1")
	aResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("asset download: %v", err)
	}
	assetBytes, _ := io.ReadAll(aResp.Body)
	aResp.Body.Close()
	if aResp.StatusCode != http.StatusOK || len(assetBytes) == 0 {
		t.Fatalf("asset download: status %d, %d bytes", aResp.StatusCode, len(assetBytes))
	}

	// 7. Roll back and observe the directive on the next poll.
	rbPath := "/" + s.appId + "/rollback/production?platform=ios&runtimeVersion=1&commitHash=integration"
	resp = s.cliRequest(t, "POST", rbPath, s.apiKey, nil, "")
	msg, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: %d: %s", resp.StatusCode, msg)
	}
	dResp, dBody := s.deviceManifest(t, "production", "1", map[string]string{
		"expo-embedded-update-id": "00000000-0000-0000-0000-000000000000",
	})
	if dResp.StatusCode != http.StatusOK {
		t.Fatalf("post-rollback poll: %d: %s", dResp.StatusCode, dBody)
	}
	if !strings.Contains(string(dBody), "rollBackToEmbedded") {
		t.Fatalf("expected rollBackToEmbedded directive, got: %s", dBody)
	}
}

// fixtureFileNames mirrors what eoas sends: every asset + bundle listed in the
// update's metadata.json, plus metadata.json and expoConfig.json themselves.
func fixtureFileNames(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "metadata.json"))
	if err != nil {
		t.Fatalf("opening fixture metadata: %v", err)
	}
	defer f.Close()
	var metadata types.MetadataObject
	if err := json.NewDecoder(f).Decode(&metadata); err != nil {
		t.Fatalf("decoding fixture metadata: %v", err)
	}
	names := []string{"metadata.json", "expoConfig.json"}
	for _, a := range metadata.FileMetadata.IOS.Assets {
		names = append(names, a.Path)
	}
	for _, a := range metadata.FileMetadata.Android.Assets {
		names = append(names, a.Path)
	}
	if metadata.FileMetadata.IOS.Bundle != "" {
		names = append(names, metadata.FileMetadata.IOS.Bundle)
	}
	if metadata.FileMetadata.Android.Bundle != "" {
		names = append(names, metadata.FileMetadata.Android.Bundle)
	}
	return names
}

func extractLaunchAssetURL(t *testing.T, manifest string) string {
	t.Helper()
	i := strings.Index(manifest, `"launchAsset"`)
	if i < 0 {
		t.Fatal("no launchAsset in manifest")
	}
	j := strings.Index(manifest[i:], `"url":"`)
	if j < 0 {
		t.Fatal("no url in launchAsset")
	}
	rest := manifest[i+j+len(`"url":"`):]
	end := strings.Index(rest, `"`)
	return strings.ReplaceAll(rest[:end], `\u0026`, "&")
}

func TestCreateChannelWithUnknownBranchReturns404(t *testing.T) {
	// Mapping a channel to a branch that doesn't exist yet (the branch is
	// created by the first publish) must be a 404, not a 500.
	s := newStack(t)
	resp, raw := s.adminJSON(t, "POST", "/api/apps/"+s.appId+"/channels", `{"channelName":"production","branchName":"never-published"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown branch, got %d: %s", resp.StatusCode, raw)
	}
}
