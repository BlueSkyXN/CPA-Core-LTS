package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	objectStoreConfigKey  = "config/config.yaml"
	objectStoreAuthPrefix = "auths"
)

var (
	errObjectStoreAuthRootIdentityUnavailable = errors.New("object store: auth root identity is not initialized")
	errObjectStoreAuthRootChanged             = errors.New("object store: auth root identity changed after initialization")
	errObjectStoreAuthRootUnavailable         = errors.New("object store: initialized auth root is unavailable")
	errObjectStoreSpoolRootChanged            = errors.New("object store: spool root identity changed during initialization")
)

func secureAuthRootOpenError(err error, expected secureAuthRootIdentity) error {
	if err == nil || !expected.valid {
		return err
	}
	return fmt.Errorf("%w: %v", errObjectStoreAuthRootUnavailable, err)
}

// ObjectStoreConfig captures configuration for the object storage-backed token store.
type ObjectStoreConfig struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
	LocalRoot string
	UseSSL    bool
	PathStyle bool
}

// ObjectTokenStore persists configuration and authentication metadata using an S3-compatible object storage backend.
// Files are mirrored to a local workspace so existing file-based flows continue to operate.
type ObjectTokenStore struct {
	client     *minio.Client
	cfg        ObjectStoreConfig
	spoolRoot  string
	configPath string
	authDir    string
	authRoot   secureAuthRootIdentity
	renderDir  string
	renderRoot secureAuthRootIdentity
	mu         sync.Mutex

	// beforeAuthWriteHook is a test seam for validating path-swap resistance.
	beforeAuthWriteHook  func()
	beforeAuthReadHook   func()
	beforeAuthDeleteHook func()
}

// NewObjectTokenStore initializes an object storage backed token store.
func NewObjectTokenStore(cfg ObjectStoreConfig) (*ObjectTokenStore, error) {
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.AccessKey = strings.TrimSpace(cfg.AccessKey)
	cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("object store: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("object store: bucket is required")
	}
	if cfg.AccessKey == "" {
		return nil, fmt.Errorf("object store: access key is required")
	}
	if cfg.SecretKey == "" {
		return nil, fmt.Errorf("object store: secret key is required")
	}

	root := strings.TrimSpace(cfg.LocalRoot)
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = filepath.Join(cwd, "objectstore")
		} else {
			root = filepath.Join(os.TempDir(), "objectstore")
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("object store: resolve spool directory: %w", err)
	}

	configDir := filepath.Join(absRoot, "config")
	authDir := filepath.Join(absRoot, "auths")

	if err = os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("object store: create config directory: %w", err)
	}
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return nil, fmt.Errorf("object store: create auth directory: %w", err)
	}
	spoolRootIdentity, err := captureSecureAuthRootIdentity(absRoot)
	if err != nil {
		return nil, fmt.Errorf("object store: pin spool directory identity: %w", err)
	}
	authRoot, err := captureSecureAuthRootIdentity(authDir)
	if err != nil {
		return nil, fmt.Errorf("object store: pin auth directory identity: %w", err)
	}
	renderDir, renderRoot, err := prepareAuthRenderStaging(absRoot, spoolRootIdentity)
	if err != nil {
		return nil, fmt.Errorf("object store: prepare auth render staging: %w", err)
	}

	options := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	if cfg.PathStyle {
		options.BucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(cfg.Endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("object store: create client: %w", err)
	}

	return &ObjectTokenStore{
		client:     client,
		cfg:        cfg,
		spoolRoot:  absRoot,
		configPath: filepath.Join(configDir, "config.yaml"),
		authDir:    authDir,
		authRoot:   authRoot,
		renderDir:  renderDir,
		renderRoot: renderRoot,
	}, nil
}

// SetBaseDir implements the optional interface used by authenticators; it is a no-op because
// the object store controls its own workspace.
func (s *ObjectTokenStore) SetBaseDir(string) {}

// ConfigPath returns the managed configuration file path inside the spool directory.
func (s *ObjectTokenStore) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// AuthDir returns the local directory containing mirrored auth files.
func (s *ObjectTokenStore) AuthDir() string {
	if s == nil {
		return ""
	}
	return s.authDir
}

// Bootstrap ensures the target bucket exists and synchronizes data from the object storage backend.
func (s *ObjectTokenStore) Bootstrap(ctx context.Context, exampleConfigPath string) error {
	if s == nil {
		return fmt.Errorf("object store: not initialized")
	}
	if err := s.ensureBucket(ctx); err != nil {
		return err
	}
	if err := s.syncConfigFromBucket(ctx, exampleConfigPath); err != nil {
		return err
	}
	if err := s.syncAuthFromBucket(ctx); err != nil {
		return err
	}
	return nil
}

// Save persists authentication metadata to disk and uploads it to the object storage backend.
func (s *ObjectTokenStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("object store: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("object store: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		rel, errRel := s.authRelativePath(path)
		if errRel != nil {
			return "", fmt.Errorf("object store: resolve disabled auth path: %w", errRel)
		}
		if s.beforeAuthReadHook != nil {
			s.beforeAuthReadHook()
		}
		_, _, errRead := secureReadAuthFile(s.authDir, s.authRoot, rel)
		if errors.Is(errRead, fs.ErrNotExist) {
			return "", nil
		}
		if errRead != nil {
			return "", fmt.Errorf("object store: inspect disabled auth file: %w", errRead)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rel, err := s.authRelativePath(path)
	if err != nil {
		return "", fmt.Errorf("object store: resolve auth relative path: %w", err)
	}

	var raw []byte
	writeLocal := true
	switch {
	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(auth.Metadata)
		}
		raw, writeLocal, err = renderAuthStorage(auth.Storage, s.renderDir, s.renderRoot)
		if err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		var errMarshal error
		raw, errMarshal = json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("object store: marshal metadata: %w", errMarshal)
		}
		if existing, _, errRead := secureReadAuthFile(s.authDir, s.authRoot, rel); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
		} else if !errors.Is(errRead, fs.ErrNotExist) {
			return "", fmt.Errorf("object store: read existing metadata: %w", errRead)
		}
	default:
		return "", fmt.Errorf("object store: nothing to persist for %s", auth.ID)
	}
	if writeLocal {
		if s.beforeAuthWriteHook != nil {
			s.beforeAuthWriteHook()
		}
		if err = secureWriteAuthFile(s.authDir, s.authRoot, rel, raw); err != nil {
			return "", fmt.Errorf("object store: write auth file: %w", err)
		}
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[cliproxyauth.AttributePath] = path
	auth.Attributes[cliproxyauth.AttributeSourceBackend] = cliproxyauth.AuthSourceObjectStore

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	if err = s.uploadAuth(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

// List enumerates auth JSON files from the mirrored workspace.
func (s *ObjectTokenStore) List(_ context.Context) ([]*cliproxyauth.Auth, error) {
	dir := strings.TrimSpace(s.AuthDir())
	if dir == "" {
		return nil, fmt.Errorf("object store: auth directory not configured")
	}
	if err := validateSecureAuthRootPath(dir, s.authRoot); err != nil {
		return nil, fmt.Errorf("object store: validate auth directory: %w", err)
	}
	entries := make([]*cliproxyauth.Auth, 0, 32)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		auth, err := s.readAuthFile(path)
		if err != nil {
			log.WithError(err).Warnf("object store: skip auth %s", path)
			return nil
		}
		if auth != nil {
			entries = append(entries, auth)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("object store: walk auth directory: %w", err)
	}
	return entries, nil
}

// Delete removes an auth file locally and remotely.
func (s *ObjectTokenStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("object store: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rel, err := s.authRelativePath(path)
	if err != nil {
		return fmt.Errorf("object store: resolve auth relative path: %w", err)
	}
	if s.beforeAuthDeleteHook != nil {
		s.beforeAuthDeleteHook()
	}
	if err = secureRemoveAuthFile(s.authDir, s.authRoot, rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("object store: delete auth file: %w", err)
	}
	if err = s.deleteAuthObjectRelative(ctx, rel); err != nil {
		return err
	}
	return nil
}

// PersistAuthFiles uploads the provided auth files to the object storage backend.
func (s *ObjectTokenStore) PersistAuthFiles(ctx context.Context, _ string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		abs := trimmed
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(s.authDir, trimmed)
		}
		if err := s.uploadAuth(ctx, abs); err != nil {
			return err
		}
	}
	return nil
}

// PersistConfig uploads the local configuration file to the object storage backend.
func (s *ObjectTokenStore) PersistConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteObject(ctx, objectStoreConfigKey)
		}
		return fmt.Errorf("object store: read config file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteObject(ctx, objectStoreConfigKey)
	}
	return s.putObject(ctx, objectStoreConfigKey, data, "application/x-yaml")
}

func (s *ObjectTokenStore) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("object store: check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err = s.client.MakeBucket(ctx, s.cfg.Bucket, minio.MakeBucketOptions{Region: s.cfg.Region}); err != nil {
		return fmt.Errorf("object store: create bucket: %w", err)
	}
	return nil
}

func (s *ObjectTokenStore) syncConfigFromBucket(ctx context.Context, example string) error {
	key := s.prefixedKey(objectStoreConfigKey)
	_, err := s.client.StatObject(ctx, s.cfg.Bucket, key, minio.StatObjectOptions{})
	switch {
	case err == nil:
		object, errGet := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
		if errGet != nil {
			return fmt.Errorf("object store: fetch config: %w", errGet)
		}
		defer object.Close()
		data, errRead := io.ReadAll(object)
		if errRead != nil {
			return fmt.Errorf("object store: read config: %w", errRead)
		}
		if errWrite := os.WriteFile(s.configPath, normalizeLineEndingsBytes(data), 0o600); errWrite != nil {
			return fmt.Errorf("object store: write config: %w", errWrite)
		}
	case isObjectNotFound(err):
		if _, statErr := os.Stat(s.configPath); errors.Is(statErr, fs.ErrNotExist) {
			if example != "" {
				if errCopy := misc.CopyConfigTemplate(example, s.configPath); errCopy != nil {
					return fmt.Errorf("object store: copy example config: %w", errCopy)
				}
			} else {
				if errCreate := os.MkdirAll(filepath.Dir(s.configPath), 0o700); errCreate != nil {
					return fmt.Errorf("object store: prepare config directory: %w", errCreate)
				}
				if errWrite := os.WriteFile(s.configPath, []byte{}, 0o600); errWrite != nil {
					return fmt.Errorf("object store: create empty config: %w", errWrite)
				}
			}
		}
		data, errRead := os.ReadFile(s.configPath)
		if errRead != nil {
			return fmt.Errorf("object store: read local config: %w", errRead)
		}
		if len(data) > 0 {
			if errPut := s.putObject(ctx, objectStoreConfigKey, data, "application/x-yaml"); errPut != nil {
				return errPut
			}
		}
	default:
		return fmt.Errorf("object store: stat config: %w", err)
	}
	return nil
}

func (s *ObjectTokenStore) syncAuthFromBucket(ctx context.Context) error {
	// NOTE: We intentionally do NOT use os.RemoveAll here.
	// Wiping the directory triggers file watcher delete events, which then
	// propagate deletions to the remote object store (race condition).
	// Instead, we just ensure the directory exists and overwrite files incrementally.
	if err := os.MkdirAll(s.authDir, 0o700); err != nil {
		return fmt.Errorf("object store: create auth directory: %w", err)
	}

	prefix := s.prefixedKey(objectStoreAuthPrefix + "/")
	objectCh := s.client.ListObjects(ctx, s.cfg.Bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	})
	for object := range objectCh {
		if object.Err != nil {
			return fmt.Errorf("object store: list auth objects: %w", object.Err)
		}
		rel := strings.TrimPrefix(object.Key, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		relPath := filepath.FromSlash(rel)
		if filepath.IsAbs(relPath) {
			log.WithField("key", object.Key).Warn("object store: skip auth outside mirror")
			continue
		}
		cleanRel := filepath.Clean(relPath)
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
			log.WithField("key", object.Key).Warn("object store: skip auth outside mirror")
			continue
		}
		reader, errGet := s.client.GetObject(ctx, s.cfg.Bucket, object.Key, minio.GetObjectOptions{})
		if errGet != nil {
			return fmt.Errorf("object store: download auth %s: %w", object.Key, errGet)
		}
		data, errRead := io.ReadAll(reader)
		_ = reader.Close()
		if errRead != nil {
			return fmt.Errorf("object store: read auth %s: %w", object.Key, errRead)
		}
		_, errWrite := s.writeAuthMirrorFile(cleanRel, data)
		if errWrite != nil {
			return fmt.Errorf("object store: write auth %s: %w", cleanRel, errWrite)
		}
	}
	return nil
}

func (s *ObjectTokenStore) writeAuthMirrorFile(relativePath string, data []byte) (string, error) {
	local, err := s.authPath(relativePath)
	if err != nil {
		return "", err
	}
	if s.beforeAuthWriteHook != nil {
		s.beforeAuthWriteHook()
	}
	if err = secureWriteAuthFile(s.authDir, s.authRoot, relativePath, data); err != nil {
		return "", err
	}
	return local, nil
}

func renderAuthStorage(storage interface{ SaveTokenToFile(string) error }, stagingDir string, stagingRoot secureAuthRootIdentity) ([]byte, bool, error) {
	// Plugin-backed storage can already render its merged JSON without touching
	// a pathname. Prefer that capability so atomic path writers do not need a
	// compatibility staging path at all.
	if renderer, ok := storage.(interface{ RawJSON() []byte }); ok {
		if raw := bytes.Clone(renderer.RawJSON()); len(bytes.TrimSpace(raw)) > 0 {
			return raw, true, nil
		}
	}

	// Legacy TokenStorage exposes only SaveTokenToFile. The OS-specific renderer
	// gives it a non-redirectable target: an unlinked descriptor alias on POSIX,
	// or a pre-opened file beneath rename/delete-locked handles on Windows.
	return renderAuthStorageIsolated(storage, stagingDir, stagingRoot)
}

func (s *ObjectTokenStore) uploadAuth(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	rel, err := s.authRelativePath(path)
	if err != nil {
		return fmt.Errorf("object store: resolve auth relative path: %w", err)
	}
	if s.beforeAuthReadHook != nil {
		s.beforeAuthReadHook()
	}
	data, _, err := secureReadAuthFile(s.authDir, s.authRoot, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteAuthObjectRelative(ctx, rel)
		}
		return fmt.Errorf("object store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthObjectRelative(ctx, rel)
	}
	key := objectStoreAuthPrefix + "/" + filepath.ToSlash(rel)
	return s.putObject(ctx, key, data, "application/json")
}

func (s *ObjectTokenStore) deleteAuthObject(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	rel, err := s.authRelativePath(path)
	if err != nil {
		return fmt.Errorf("object store: resolve auth relative path: %w", err)
	}
	return s.deleteAuthObjectRelative(ctx, rel)
}

func (s *ObjectTokenStore) deleteAuthObjectRelative(ctx context.Context, rel string) error {
	key := objectStoreAuthPrefix + "/" + filepath.ToSlash(rel)
	return s.deleteObject(ctx, key)
}

func (s *ObjectTokenStore) putObject(ctx context.Context, key string, data []byte, contentType string) error {
	if len(data) == 0 {
		return s.deleteObject(ctx, key)
	}
	fullKey := s.prefixedKey(key)
	reader := bytes.NewReader(data)
	_, err := s.client.PutObject(ctx, s.cfg.Bucket, fullKey, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("object store: put object %s: %w", fullKey, err)
	}
	return nil
}

func (s *ObjectTokenStore) deleteObject(ctx context.Context, key string) error {
	fullKey := s.prefixedKey(key)
	err := s.client.RemoveObject(ctx, s.cfg.Bucket, fullKey, minio.RemoveObjectOptions{})
	if err != nil {
		if isObjectNotFound(err) {
			return nil
		}
		return fmt.Errorf("object store: delete object %s: %w", fullKey, err)
	}
	return nil
}

func (s *ObjectTokenStore) prefixedKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.cfg.Prefix == "" {
		return key
	}
	return strings.TrimLeft(s.cfg.Prefix+"/"+key, "/")
}

func (s *ObjectTokenStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("object store: auth is nil")
	}
	if auth.Attributes != nil {
		if path := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath]); path != "" {
			return s.authPath(path)
		}
	}
	fileName := strings.TrimSpace(auth.FileName)
	if fileName == "" {
		fileName = strings.TrimSpace(auth.ID)
	}
	if fileName == "" {
		return "", fmt.Errorf("object store: auth %s missing filename", auth.ID)
	}
	if !strings.HasSuffix(strings.ToLower(fileName), ".json") {
		fileName += ".json"
	}
	return s.authPath(fileName)
}

func (s *ObjectTokenStore) resolveDeletePath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("object store: id is empty")
	}
	if filepath.IsAbs(id) {
		return s.authPath(id)
	}
	// Treat any non-absolute id (including nested like "team/foo") as relative to the mirror authDir.
	// Normalize separators and guard against path traversal.
	clean := filepath.Clean(filepath.FromSlash(id))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("object store: invalid auth identifier %s", id)
	}
	// Ensure .json suffix.
	if !strings.HasSuffix(strings.ToLower(clean), ".json") {
		clean += ".json"
	}
	return s.authPath(clean)
}

func (s *ObjectTokenStore) authPath(path string) (string, error) {
	if s == nil || strings.TrimSpace(s.authDir) == "" {
		return "", fmt.Errorf("object store: auth directory not configured")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.authDir, filepath.FromSlash(candidate))
	}
	rel, err := s.authRelativePath(candidate)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.authDir, rel), nil
}

func (s *ObjectTokenStore) authRelativePath(path string) (string, error) {
	if s == nil || strings.TrimSpace(s.authDir) == "" {
		return "", fmt.Errorf("object store: auth directory not configured")
	}
	authDir, err := filepath.Abs(s.authDir)
	if err != nil {
		return "", err
	}
	authPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(authDir, authPath)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("auth path %q escapes auth directory", path)
	}
	if err = ensureResolvedPathWithin(authDir, authPath); err != nil {
		return "", err
	}
	return rel, nil
}

func ensureResolvedPathWithin(baseDir, path string) error {
	// This preflight rejects pre-existing symlink components, including dangling
	// leaf symlinks. Auth-file writes additionally use descriptor- or
	// handle-relative no-follow I/O to close the check/write race.
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve auth directory: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve auth path %q: %w", path, err)
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("auth path %q escapes auth directory", path)
	}

	current := absBase
	for _, component := range strings.Split(rel, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, errLstat := os.Lstat(current)
		if errors.Is(errLstat, fs.ErrNotExist) {
			// Once a component is absent, no deeper pre-existing symlink can be
			// traversed by the eventual create.
			return nil
		}
		if errLstat != nil {
			return fmt.Errorf("inspect auth path %q: %w", path, errLstat)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("auth path %q contains symlink component %q", path, current)
		}
	}
	return nil
}

func (s *ObjectTokenStore) readAuthFile(path string) (*cliproxyauth.Auth, error) {
	if s.beforeAuthReadHook != nil {
		s.beforeAuthReadHook()
	}
	rel, err := filepath.Rel(s.authDir, path)
	if err != nil {
		return nil, fmt.Errorf("resolve auth relative path: %w", err)
	}
	data, info, err := secureReadAuthFile(s.authDir, s.authRoot, rel)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	if err = json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("unmarshal auth json: %w", err)
	}
	provider := strings.TrimSpace(valueAsString(metadata["type"]))
	if provider == "" {
		provider = "unknown"
	}
	rel = normalizeAuthID(rel)
	localPath := filepath.Join(s.authDir, rel)
	attr := map[string]string{
		cliproxyauth.AttributePath:          localPath,
		cliproxyauth.AttributeSourceBackend: cliproxyauth.AuthSourceObjectStore,
	}
	if email := strings.TrimSpace(valueAsString(metadata["email"])); email != "" {
		attr["email"] = email
	}
	auth := &cliproxyauth.Auth{
		ID:               rel,
		Provider:         provider,
		FileName:         rel,
		Label:            labelFor(metadata),
		Status:           cliproxyauth.StatusActive,
		Attributes:       attr,
		Metadata:         metadata,
		CreatedAt:        info.ModTime(),
		UpdatedAt:        info.ModTime(),
		LastRefreshedAt:  time.Time{},
		NextRefreshAfter: time.Time{},
	}
	cliproxyauth.HydrateAuthFromMetadata(auth)
	return auth, nil
}

func normalizeLineEndingsBytes(data []byte) []byte {
	replaced := bytes.ReplaceAll(data, []byte{'\r', '\n'}, []byte{'\n'})
	return bytes.ReplaceAll(replaced, []byte{'\r'}, []byte{'\n'})
}

func isObjectNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == http.StatusNotFound {
		return true
	}
	switch resp.Code {
	case "NoSuchKey", "NotFound", "NoSuchBucket":
		return true
	}
	return false
}
