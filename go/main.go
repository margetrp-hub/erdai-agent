package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultCoreListenAddr  = "127.0.0.1:6280"
	defaultAdminListenAddr = "127.0.0.1:6282"
	adminTokenHeader       = "X-Erdai-Admin-Token"
	adminSessionCookie     = "erdai_admin_session"
	adminSessionTTL        = 8 * time.Hour
)

// The UI is embedded so the gateway has no runtime filesystem dependency.
//
//go:embed webui/dist
var webFiles embed.FS

var allowedMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodDelete: true,
}

type Gateway struct {
	static            http.Handler
	assetETag         string
	adminToken        string
	adminUsername     string
	adminPasswordHash string
	runtime           *AgentRuntime
	sessionMu         sync.Mutex
	adminSessions     map[string]time.Time
}

func main() {
	if handled, err := handleCLI(os.Args[1:]); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	coreListen := envOr("ERDAI_CORE_LISTEN", defaultCoreListenAddr)
	adminListen := envOr("ERDAI_ADMIN_LISTEN", defaultAdminListenAddr)
	if coreListen == adminListen {
		log.Fatal("ERDAI_CORE_LISTEN and ERDAI_ADMIN_LISTEN must be different")
	}
	if err := loadManagedCredentialsFile(managedCredentialPath()); err != nil {
		log.Printf("managed credentials unavailable: %v", err)
	}
	adminToken := strings.TrimSpace(os.Getenv("ERDAI_ADMIN_TOKEN"))
	if len(adminToken) < 32 {
		log.Fatal("ERDAI_ADMIN_TOKEN must contain at least 32 characters")
	}
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath:              envOr("ERDAI_RUNTIME_DATABASE", "/data/erdai-agent-core.sqlite3"),
		ConfigDatabasePath:        envOr("ERDAI_CONFIG_DATABASE", "/data/erdai-agent-core.sqlite3"),
		LegacyRuntimeDatabasePath: strings.TrimSpace(os.Getenv("ERDAI_LEGACY_RUNTIME_DATABASE")),
		AdminToken:                adminToken,
		PointsReadToken:           strings.TrimSpace(os.Getenv("ERDAI_POINTS_READ_TOKEN")),
		RuntimeToken:              os.Getenv("ERDAI_RUNTIME_TOKEN"),
		ModelAPIKey:               os.Getenv("ERDAI_MODEL_API_KEY"),
		GrokAPIKey:                os.Getenv("ERDAI_GROK_API_KEY"),
		SearchBaseURL:             envOr("ERDAI_SEARCH_BASE_URL", defaultSearchBaseURL),
		ImageAPIKey:               os.Getenv("ERDAI_IMAGE_API_KEY"),
		OpsToken:                  os.Getenv("ERDAI_OPS_TOKEN"),
		Sub2APIMonitorEmail:       os.Getenv("ERDAI_SUB2API_MONITOR_EMAIL"),
		Sub2APIMonitorPassword:    os.Getenv("ERDAI_SUB2API_MONITOR_PASSWORD"),
		EncryptionKey:             os.Getenv("ERDAI_RUN_ENCRYPTION_KEY"),
		IdentitySecret:            os.Getenv("ERDAI_RUNTIME_IDENTITY_SECRET"),
		MediaDir:                  envOr("ERDAI_MEDIA_DIR", "/data/media"),
		UpdateRepository:          envOr("ERDAI_UPDATE_REPOSITORY", "margetrp-hub/erdai-agent"),
		UpdateAPIBaseURL:          envOr("ERDAI_UPDATE_API_BASE_URL", "https://api.github.com"),
		ModelTimeout:              parseDurationSeconds(os.Getenv("ERDAI_MODEL_TIMEOUT_SECONDS"), 120),
	})
	if err != nil {
		log.Fatal(err)
	}
	adminUsername := strings.TrimSpace(os.Getenv("ERDAI_ADMIN_USERNAME"))
	adminPasswordHash := strings.TrimSpace(os.Getenv("ERDAI_ADMIN_PASSWORD_SHA256"))
	if adminPasswordHash == "" {
		adminPasswordHash = hashAdminPassword(os.Getenv("ERDAI_ADMIN_PASSWORD"))
	}
	defer runtime.Close()
	if err = runtime.StartPlatformConnectors(context.Background()); err != nil {
		log.Printf("platform connectors unavailable: %v", err)
	}
	coreGateway := NewGateway("")
	coreGateway.runtime = runtime
	adminGateway := NewGatewayWithCredentials(adminToken, adminUsername, adminPasswordHash)
	adminGateway.runtime = runtime
	servers := []*http.Server{
		{Addr: coreListen, Handler: coreGateway, ReadHeaderTimeout: 10 * time.Second},
		{Addr: adminListen, Handler: adminGateway, ReadHeaderTimeout: 10 * time.Second},
	}
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server *http.Server) {
			log.Printf("Go Core listening on http://%s", server.Addr)
			errCh <- server.ListenAndServe()
		}(server)
	}
	var serveErr error
	select {
	case <-stop.Done():
	case serveErr = <-errCh:
		cancel()
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, server := range servers {
		_ = server.Shutdown(shutdownContext)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Fatal(serveErr)
	}
}

func handleCLI(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--health-check" {
		return true, checkHTTPHealth()
	}
	if len(args) == 1 && args[0] == "--mcp-stdio" {
		return true, serveLocalMCPStdio(os.Stdin, os.Stdout)
	}
	if len(args) != 2 || args[0] != "--check-sqlite" {
		return true, errors.New("usage: erdai-agent-core --health-check | --mcp-stdio | --check-sqlite <database-path>")
	}
	if err := checkSQLiteIntegrity(args[1]); err != nil {
		return true, err
	}
	fmt.Println("ok")
	return true, nil
}

func checkHTTPHealth() error {
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Get("http://127.0.0.1:6280/healthz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func checkSQLiteIntegrity(databasePath string) error {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return errors.New("database path is required")
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	var result string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("check database integrity: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(result)) != "ok" {
		return fmt.Errorf("database integrity check failed: %s", result)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func NewGateway(adminToken string) *Gateway {
	return NewGatewayWithCredentials(adminToken, "", "")
}

func NewGatewayWithCredentials(adminToken, adminUsername, adminPasswordHash string) *Gateway {
	adminToken = strings.TrimSpace(adminToken)
	adminUsername = strings.TrimSpace(adminUsername)
	adminPasswordHash = strings.TrimSpace(adminPasswordHash)
	staticFS, err := fs.Sub(webFiles, "webui/dist")
	if err != nil {
		panic(fmt.Sprintf("embedded WebUI unavailable: %v", err))
	}
	index, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		panic(fmt.Sprintf("embedded WebUI index unavailable: %v", err))
	}
	digest := sha256.Sum256(index)
	etag := fmt.Sprintf(`"erdai-webui-%x"`, digest[:8])
	gateway := &Gateway{static: http.FileServer(http.FS(staticFS)), assetETag: etag, adminToken: adminToken, adminUsername: adminUsername, adminPasswordHash: adminPasswordHash}
	if adminToken != "" || (adminUsername != "" && adminPasswordHash != "") {
		gateway.adminSessions = map[string]time.Time{}
	}
	return gateway
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if (g.adminToken != "" || (g.adminUsername != "" && g.adminPasswordHash != "")) && strings.HasPrefix(r.URL.Path, "/auth/") {
		g.handleAdminAuth(w, r)
		return
	}
	if g.runtime != nil && g.runtime.realtime != nil &&
		g.runtime.realtime.handlePublic(w, r, cleanPath(r.URL.Path)) {
		return
	}
	if g.runtime != nil && g.runtime.localMCP != nil &&
		g.runtime.localMCP.handleHTTP(w, r, cleanPath(r.URL.Path), g.runtime.authorized(r)) {
		return
	}
	if g.runtime != nil && g.runtime.handlePointsReadBridge(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path != cleanPath(r.URL.Path) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
			return
		}
		if !allowedMethods[r.Method] {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if g.adminToken == "" && !isRuntimeTransportPath(r.URL.Path) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error": map[string]string{"code": "not_found", "message": "route not found"},
			})
			return
		}
		if g.runtime != nil {
			runtimeRequest := r.Clone(r.Context())
			runtimeRequest.Header = r.Header.Clone()
			if g.adminToken != "" {
				headerAuthenticated := tokenMatches(r.Header.Get(adminTokenHeader), g.adminToken)
				sessionAuthenticated := g.validAdminSession(r)
				if !headerAuthenticated && !sessionAuthenticated {
					writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error": map[string]string{"code": "unauthorized", "message": "administrator login required"},
					})
					return
				}
				if sessionAuthenticated && !headerAuthenticated && isUnsafeMethod(r.Method) && !sameOriginRequest(r) {
					writeJSON(w, http.StatusForbidden, map[string]any{
						"error": map[string]string{"code": "forbidden", "message": "same-origin request required"},
					})
					return
				}
				runtimeRequest.Header.Del(adminTokenHeader)
				runtimeRequest.Header.Set(adminTokenHeader, g.adminToken)
			}
			if g.runtime.Handle(w, runtimeRequest) {
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]string{"code": "not_found", "message": "route not found"},
		})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	setStaticHeaders(w)
	w.Header().Set("etag", g.assetETag)
	if r.Header.Get("if-none-match") == g.assetETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	g.static.ServeHTTP(w, r)
}

func (g *Gateway) handleAdminAuth(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/auth/session":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"authenticated": g.validAdminSession(r)}})
	case "/auth/login":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var payload struct {
			Token    string `json:"token"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || !g.adminCredentialsMatch(payload.Token, payload.Username, payload.Password) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{"code": "unauthorized", "message": "administrator login failed"},
			})
			return
		}
		sessionID, err := g.createAdminSession()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "internal_error", "message": "could not create administrator session"},
			})
			return
		}
		g.setAdminSessionCookie(w, r, sessionID, int(adminSessionTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"authenticated": true}})
	case "/auth/logout":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if cookie, err := r.Cookie(adminSessionCookie); err == nil {
			g.sessionMu.Lock()
			delete(g.adminSessions, cookie.Value)
			g.sessionMu.Unlock()
		}
		g.setAdminSessionCookie(w, r, "", -1)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"authenticated": false}})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
	}
}

func (g *Gateway) adminCredentialsMatch(token, username, password string) bool {
	if tokenMatches(token, g.adminToken) {
		return true
	}
	return tokenMatches(username, g.adminUsername) && passwordHashMatches(password, g.adminPasswordHash)
}

func hashAdminPassword(password string) string {
	password = strings.TrimSpace(password)
	if password == "" {
		return ""
	}
	const iterations = 120000
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return ""
	}
	derived := derivePasswordKey(password, salt, iterations)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations,
		base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(derived))
}

func passwordHashMatches(password, expectedHash string) bool {
	expectedHash = strings.TrimSpace(expectedHash)
	if expectedHash == "" {
		return false
	}
	parts := strings.Split(expectedHash, "$")
	if len(parts) == 4 && parts[0] == "pbkdf2-sha256" {
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations < 100000 || iterations > 1000000 {
			return false
		}
		salt, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(salt) < 16 {
			return false
		}
		stored, err := base64.RawURLEncoding.DecodeString(parts[3])
		if err != nil || len(stored) != sha256.Size {
			return false
		}
		derived := derivePasswordKey(strings.TrimSpace(password), salt, iterations)
		return subtle.ConstantTimeCompare(derived, stored) == 1
	}
	return tokenMatches(legacyPasswordHash(password), strings.ToLower(expectedHash))
}

func derivePasswordKey(password string, salt []byte, iterations int) []byte {
	const keyLength = sha256.Size
	result := make([]byte, 0, keyLength)
	for block := uint32(1); len(result) < keyLength; block++ {
		mac := hmac.New(sha256.New, []byte(password))
		_, _ = mac.Write(salt)
		_, _ = mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		accumulator := append([]byte(nil), u...)
		for index := 1; index < iterations; index++ {
			mac = hmac.New(sha256.New, []byte(password))
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for offset := range accumulator {
				accumulator[offset] ^= u[offset]
			}
		}
		result = append(result, accumulator...)
	}
	return result[:keyLength]
}

func legacyPasswordHash(password string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(password)))
	return hex.EncodeToString(digest[:])
}

func (g *Gateway) createAdminSession() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	sessionID := base64.RawURLEncoding.EncodeToString(raw)
	g.sessionMu.Lock()
	g.adminSessions[sessionID] = time.Now().Add(adminSessionTTL)
	g.sessionMu.Unlock()
	return sessionID, nil
}

func (g *Gateway) validAdminSession(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	now := time.Now()
	g.sessionMu.Lock()
	defer g.sessionMu.Unlock()
	expiresAt, found := g.adminSessions[cookie.Value]
	if !found || !expiresAt.After(now) {
		delete(g.adminSessions, cookie.Value)
		return false
	}
	return true
}

func (g *Gateway) setAdminSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	secure := r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	expectedHost := r.Host
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
		expectedHost = forwarded
	}
	return strings.EqualFold(parsed.Host, expectedHost)
}

func cleanPath(path string) string {
	return pathpkg.Clean(path)
}

func setStaticHeaders(w http.ResponseWriter) {
	w.Header().Set("cache-control", "private, max-age=0, must-revalidate")
	w.Header().Set("content-security-policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("referrer-policy", "no-referrer")
	w.Header().Set("x-content-type-options", "nosniff")
	w.Header().Set("x-frame-options", "DENY")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
