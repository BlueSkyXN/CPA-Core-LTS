package managementasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	defaultManagementReleaseURL  = "https://api.github.com/repos/BlueSkyXN/CPA-Panel-LTS/releases/latest"
	defaultManagementFallbackURL = "https://github.com/BlueSkyXN/CPA-Panel-LTS/releases/latest/download/management.html"
	managementAssetName          = "management.html"
	httpUserAgent                = "CLIProxyAPI-management-updater"
	managementSyncMinInterval    = 30 * time.Second
	updateCheckInterval          = 3 * time.Hour
	maxAssetDownloadSize         = 50 << 20 // 10 MB safety limit for management asset downloads
)

// ManagementFileName exposes the control panel asset filename.
const ManagementFileName = managementAssetName

var (
	updateMu            sync.Mutex
	configuredPanels    = make(map[string]panelSettings)
	panelUpdates        = make(map[string]*panelUpdate)
	schedulerOnce       sync.Once
	schedulerConfigPath atomic.Value
	httpClientFactory   = newHTTPClient
	panelRepositorySlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

type panelUpdate struct {
	releaseURL string
	started    time.Time
	done       chan struct{}
}

// Each server publishes only the settings needed by its panel updater.
type panelSettings struct {
	releaseURL string
	proxyURL   string
	skipReason string
}

// The caller holds updateMu. The empty key is the process-wide default.
func panelSettingsLocked(target string) (panelSettings, bool) {
	settings, ok := configuredPanels[target]
	if !ok {
		settings, ok = configuredPanels[""]
	}
	return settings, ok
}

func isHTTPSGitHubURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "github.com" || host == "api.github.com"
}

func isGitHubRepositoryReference(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "git@github.com:") {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "github.com" || host == "api.github.com"
}

func panelAuthorizationHeader(releaseURL string) string {
	if !isHTTPSGitHubURL(releaseURL) {
		return ""
	}
	if token := strings.TrimSpace(os.Getenv("CLIPROXYAPI_PANEL_GITHUB_TOKEN")); token != "" {
		return "Bearer " + token
	}
	if token := githubauth.TokenForURL(releaseURL); token != "" {
		return "Bearer " + token
	}
	if token := strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN")); token != "" && isGitHubRepositoryReference(os.Getenv("GITSTORE_GIT_URL")) {
		return "Bearer " + token
	}
	return ""
}

// SetCurrentConfig stores the latest configuration snapshot for management asset decisions.
func SetCurrentConfig(cfg *config.Config) {
	setPanelConfig(cfg, "")
}

// SetCurrentConfigForFile scopes repository changes to this server's panel file.
func SetCurrentConfigForFile(cfg *config.Config, configFilePath string) {
	dir := StaticDir(configFilePath)
	if dir == "" {
		SetCurrentConfig(cfg)
		return
	}
	setPanelConfig(cfg, panelUpdatePath(dir))
}

func setPanelConfig(cfg *config.Config, target string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if cfg != nil {
		source := resolveReleaseURL(cfg.RemoteManagement.PanelGitHubRepository)
		reason, _ := autoUpdateSkipReason(cfg)
		if source == "" && reason == "" {
			reason = "invalid panel-github-repository"
		}
		old, ok := configuredPanels[target]
		configuredPanels[target] = panelSettings{releaseURL: source, proxyURL: cfg.ProxyURL, skipReason: reason}
		if ok && strings.EqualFold(old.releaseURL, source) {
			return
		}
	} else {
		delete(configuredPanels, target)
	}
	if target != "" {
		delete(panelUpdates, target)
	} else {
		// The process-wide setting applies to destinations without a server configuration.
		for file := range panelUpdates {
			if _, scoped := configuredPanels[file]; !scoped {
				delete(panelUpdates, file)
			}
		}
	}
}

// StartAutoUpdater launches a background goroutine that periodically ensures the management asset is up to date.
// It respects the disable-control-panel flag on every iteration and supports hot-reloaded configurations.
func StartAutoUpdater(ctx context.Context, configFilePath string) {
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		log.Debug("management asset auto-updater skipped: empty config path")
		return
	}

	schedulerConfigPath.Store(configFilePath)

	schedulerOnce.Do(func() {
		go runAutoUpdater(ctx)
	})
}

func runAutoUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(updateCheckInterval)
	defer ticker.Stop()

	runOnce := func() {
		configPath, _ := schedulerConfigPath.Load().(string)
		staticDir := StaticDir(configPath)
		target := panelUpdatePath(staticDir)
		updateMu.Lock()
		settings, ok := panelSettingsLocked(target)
		updateMu.Unlock()
		if !ok {
			log.Debug("management asset auto-updater skipped: config not yet available")
			return
		}
		if settings.skipReason != "" {
			log.Debugf("management asset auto-updater skipped: %s", settings.skipReason)
			return
		}
		EnsureLatestManagementHTML(ctx, staticDir, settings.proxyURL, settings.releaseURL)
	}

	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func autoUpdateSkipReason(cfg *config.Config) (string, bool) {
	if cfg == nil {
		return "config not yet available", true
	}
	if cfg.Home.Enabled {
		return "cluster mode enabled", true
	}
	if cfg.RemoteManagement.DisableControlPanel {
		return "control panel disabled", true
	}
	if cfg.RemoteManagement.DisableAutoUpdatePanel {
		return "disable-auto-update-panel is enabled", true
	}
	return "", false
}

func newHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 15 * time.Second}

	sdkCfg := &sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}
	util.SetProxy(sdkCfg, client)

	return client
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type releaseResponse struct {
	Assets []releaseAsset `json:"assets"`
}

// StaticDir resolves the directory that stores the management control panel asset.
func StaticDir(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return filepath.Dir(cleaned)
		}
		return cleaned
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "static")
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	fileInfo, err := os.Stat(configFilePath)
	if err == nil {
		if fileInfo.IsDir() {
			base = configFilePath
		}
	}

	return filepath.Join(base, "static")
}

// FilePath resolves the absolute path to the management control panel asset.
func FilePath(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return cleaned
		}
		return filepath.Join(cleaned, ManagementFileName)
	}

	dir := StaticDir(configFilePath)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ManagementFileName)
}

func panelUpdatePath(staticDir string) string {
	target := filepath.Join(staticDir, managementAssetName)
	if absolute, err := filepath.Abs(target); err == nil {
		return absolute
	}
	return target
}

// beginPanelUpdate returns the shared attempt and whether the caller owns the download.
func beginPanelUpdate(target, source string) (*panelUpdate, bool) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if configured, ok := panelSettingsLocked(target); ok && !strings.EqualFold(configured.releaseURL, source) {
		return nil, false
	}
	if source == "" {
		delete(panelUpdates, target)
		return nil, false
	}
	now := time.Now()
	for file, previous := range panelUpdates {
		if now.Sub(previous.started) >= managementSyncMinInterval {
			select {
			case <-previous.done:
				delete(panelUpdates, file)
			default:
			}
		}
	}
	if current := panelUpdates[target]; current != nil && strings.EqualFold(current.releaseURL, source) {
		return current, false
	}
	update := &panelUpdate{releaseURL: source, started: now, done: make(chan struct{})}
	panelUpdates[target] = update
	return update, true
}

// EnsureLatestManagementHTML coalesces checks for the same file and repository, and returns
// whether a local panel is available after the attempt.
func EnsureLatestManagementHTML(ctx context.Context, staticDir string, proxyURL string, panelRepository string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	staticDir = strings.TrimSpace(staticDir)
	if staticDir == "" {
		log.Debug("management asset sync skipped: empty static directory")
		return false
	}
	target := panelUpdatePath(staticDir)
	source := resolveReleaseURL(panelRepository)
	update, owner := beginPanelUpdate(target, source)
	switch {
	case owner:
		defer close(update.done)
		update.download(ctx, target, proxyURL)
	case update != nil:
		<-update.done
	case source == "":
		log.Warn("invalid panel-github-repository: use a GitHub repository URL or its releases API URL")
	}
	_, err := os.Stat(target)
	return err == nil
}

func (update *panelUpdate) download(ctx context.Context, target, proxyURL string) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		log.WithError(err).Warn("failed to prepare static directory for management asset")
		return
	}
	localHash, err := fileSHA256(target)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		log.WithError(err).Debug("failed to read local management asset hash")
	}

	client := httpClientFactory(proxyURL)
	asset, remoteHash, err := fetchLatestAsset(ctx, client, update.releaseURL)
	var data []byte
	var downloadedHash string
	if err == nil {
		if remoteHash != "" && strings.EqualFold(remoteHash, localHash) {
			log.Debug("management asset is already up to date")
			return
		}
		data, downloadedHash, err = downloadAsset(ctx, client, asset.BrowserDownloadURL)
	}
	if err != nil {
		if !missing || !strings.EqualFold(update.releaseURL, defaultManagementReleaseURL) {
			log.WithError(err).Warn("failed to download management asset")
			return
		}
		log.WithError(err).Warn("failed to fetch management asset, trying fallback page")
		data, downloadedHash, err = downloadAsset(ctx, client, defaultManagementFallbackURL)
		if err != nil {
			log.WithError(err).Warn("failed to download fallback management control panel page")
			return
		}
		log.Warnf("management asset downloaded from fallback URL without digest verification (hash=%s)", downloadedHash)
	} else if remoteHash != "" && !strings.EqualFold(remoteHash, downloadedHash) {
		log.Errorf("management asset digest mismatch: expected %s got %s", remoteHash, downloadedHash)
		return
	}

	if err = update.publish(target, data); errors.Is(err, errPanelSourceChanged) {
		log.Debug("discarded management asset after panel repository changed")
	} else if err != nil {
		log.WithError(err).Warn("failed to update management asset on disk")
	} else {
		log.Infof("management asset updated successfully (hash=%s)", downloadedHash)
	}
}

// resolveReleaseURL returns the canonical latest-release URL, or an empty string
// for an unsupported explicit source. Only an empty setting selects the default implicitly.
func resolveReleaseURL(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return defaultManagementReleaseURL
	}
	if panelRepositorySlug.MatchString(strings.TrimSuffix(repo, "/")) {
		repo = "https://github.com/" + repo
	}
	parsed, err := url.Parse(repo)
	if err != nil || parsed.User != nil {
		return ""
	}
	port := parsed.Port()
	switch parsed.Scheme {
	case "https":
		if port != "" && port != "443" {
			return ""
		}
	case "http":
		if port != "" && port != "80" {
			return ""
		}
	default:
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com":
		// Repository links can also point at a page within the repository.
	case "api.github.com":
		if !strings.EqualFold(parts[0], "repos") {
			return ""
		}
		parts = parts[1:]
		if len(parts) > 2 {
			switch strings.ToLower(strings.Join(parts[2:], "/")) {
			case "releases", "releases/latest":
			default:
				return ""
			}
		}
	default:
		return ""
	}
	if len(parts) < 2 {
		return ""
	}
	slug, err := url.PathUnescape(strings.Join(parts[:2], "/"))
	slug = strings.TrimSuffix(slug, ".git")
	if err != nil || !panelRepositorySlug.MatchString(slug) || path.Clean("/"+slug) != "/"+slug {
		return ""
	}
	return "https://api.github.com/repos/" + slug + "/releases/latest"
}

func fetchLatestAsset(ctx context.Context, client *http.Client, releaseURL string) (*releaseAsset, string, error) {
	headers := map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": httpUserAgent,
	}
	if authorization := panelAuthorizationHeader(releaseURL); authorization != "" {
		headers["Authorization"] = authorization
	}

	data, err := httpfetch.GetBytes(ctx, client, releaseURL, headers, 0)
	if err != nil {
		return nil, "", fmt.Errorf("fetch release: %w", err)
	}

	var release releaseResponse
	if err = json.Unmarshal(data, &release); err != nil {
		return nil, "", fmt.Errorf("decode release response: %w", err)
	}

	for i := range release.Assets {
		asset := &release.Assets[i]
		if strings.EqualFold(asset.Name, managementAssetName) {
			remoteHash := parseDigest(asset.Digest)
			return asset, remoteHash, nil
		}
	}

	return nil, "", fmt.Errorf("management asset %s not found in latest release", managementAssetName)
}

func downloadAsset(ctx context.Context, client *http.Client, downloadURL string) ([]byte, string, error) {
	if strings.TrimSpace(downloadURL) == "" {
		return nil, "", fmt.Errorf("empty download url")
	}

	data, err := httpfetch.GetBytes(ctx, client, downloadURL, map[string]string{"User-Agent": httpUserAgent}, maxAssetDownloadSize)
	if err != nil {
		return nil, "", fmt.Errorf("download asset: %w", err)
	}

	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

var errPanelSourceChanged = errors.New("panel repository changed during download")

func (update *panelUpdate) publish(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "management-*.html")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err = tmpFile.Write(data); err != nil {
		return err
	}

	if err = tmpFile.Chmod(0o644); err != nil {
		return err
	}

	if err = tmpFile.Close(); err != nil {
		return err
	}

	// The rename and source selection share a lock. Network and temporary-file writes do not.
	updateMu.Lock()
	defer updateMu.Unlock()
	if panelUpdates[path] != update {
		return errPanelSourceChanged
	}
	return os.Rename(tmpName, path)
}

func parseDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return ""
	}

	if idx := strings.Index(digest, ":"); idx >= 0 {
		digest = digest[idx+1:]
	}

	return strings.ToLower(strings.TrimSpace(digest))
}
