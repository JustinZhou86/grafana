package signature

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/gobwas/glob"

	"github.com/grafana/grafana/pkg/plugins"
	"github.com/grafana/grafana/pkg/plugins/config"
	"github.com/grafana/grafana/pkg/plugins/log"
	"github.com/grafana/grafana/pkg/setting"
)

var (
	runningWindows = runtime.GOOS == "windows"

	// toSlash is filepath.ToSlash, but can be overwritten in tests path separators cross-platform
	toSlash = filepath.ToSlash

	// fromSlash is filepath.FromSlash, but can be overwritten in tests path separators cross-platform
	fromSlash = filepath.FromSlash
)

// PluginManifest holds details for the file manifest
type PluginManifest struct {
	Plugin  string            `json:"plugin"`
	Version string            `json:"version"`
	KeyID   string            `json:"keyId"`
	Time    int64             `json:"time"`
	Files   map[string]string `json:"files"`

	// V2 supported fields
	ManifestVersion string                `json:"manifestVersion"`
	SignatureType   plugins.SignatureType `json:"signatureType"`
	SignedByOrg     string                `json:"signedByOrg"`
	SignedByOrgName string                `json:"signedByOrgName"`
	RootURLs        []string              `json:"rootUrls"`
}

func (m *PluginManifest) isV2() bool {
	return strings.HasPrefix(m.ManifestVersion, "2.")
}

type Signature struct {
	log log.Logger
	kr  plugins.KeyRetriever
}

var _ plugins.SignatureCalculator = &Signature{}

func ProvideService(cfg *config.Cfg, kr plugins.KeyRetriever) *Signature {
	return &Signature{
		log: log.New("plugin.signature"),
		kr:  kr,
	}
}

// readPluginManifest attempts to read and verify the plugin manifest
// if any error occurs or the manifest is not valid, this will return an error
func (s *Signature) readPluginManifest(ctx context.Context, body []byte) (*PluginManifest, error) {
	block, _ := clearsign.Decode(body)
	if block == nil {
		return nil, errors.New("unable to decode manifest")
	}

	// Convert to a well typed object
	var manifest PluginManifest
	err := json.Unmarshal(block.Plaintext, &manifest)
	if err != nil {
		return nil, fmt.Errorf("%v: %w", "Error parsing manifest JSON", err)
	}

	if err = s.validateManifest(ctx, manifest, block); err != nil {
		return nil, err
	}

	return &manifest, nil
}

func (s *Signature) Calculate(ctx context.Context, src plugins.PluginSource, plugin plugins.FoundPlugin) (plugins.Signature, error) {
	if defaultSignature, exists := src.DefaultSignature(ctx); exists {
		return defaultSignature, nil
	}
	fsFiles, err := plugin.FS.Files()
	if err != nil {
		return plugins.Signature{}, fmt.Errorf("files: %w", err)
	}
	if len(fsFiles) == 0 {
		s.log.Warn("No plugin file information in directory", "pluginID", plugin.JSONData.ID)
		return plugins.Signature{
			Status: plugins.SignatureStatusInvalid,
		}, nil
	}

	f, err := plugin.FS.Open("MANIFEST.txt")
	if err != nil {
		if errors.Is(err, plugins.ErrFileNotExist) {
			s.log.Debug("Could not find a MANIFEST.txt", "id", plugin.JSONData.ID, "err", err)
			return plugins.Signature{
				Status: plugins.SignatureStatusUnsigned,
			}, nil
		}

		s.log.Debug("Could not open MANIFEST.txt", "id", plugin.JSONData.ID, "err", err)
		return plugins.Signature{
			Status: plugins.SignatureStatusInvalid,
		}, nil
	}
	defer func() {
		if f == nil {
			return
		}
		if err = f.Close(); err != nil {
			s.log.Warn("Failed to close plugin MANIFEST file", "err", err)
		}
	}()

	byteValue, err := io.ReadAll(f)
	if err != nil || len(byteValue) < 10 {
		s.log.Debug("MANIFEST.TXT is invalid", "id", plugin.JSONData.ID)
		return plugins.Signature{
			Status: plugins.SignatureStatusUnsigned,
		}, nil
	}

	manifest, err := s.readPluginManifest(ctx, byteValue)
	if err != nil {
		s.log.Warn("Plugin signature invalid", "id", plugin.JSONData.ID, "err", err)
		return plugins.Signature{
			Status: plugins.SignatureStatusInvalid,
		}, nil
	}

	if !manifest.isV2() {
		return plugins.Signature{
			Status: plugins.SignatureStatusInvalid,
		}, nil
	}

	// Make sure the versions all match
	if manifest.Plugin != plugin.JSONData.ID || manifest.Version != plugin.JSONData.Info.Version {
		return plugins.Signature{
			Status: plugins.SignatureStatusModified,
		}, nil
	}

	// Validate that plugin is running within defined root URLs
	if len(manifest.RootURLs) > 0 {
		if match, err := urlMatch(manifest.RootURLs, setting.AppUrl, manifest.SignatureType); err != nil {
			s.log.Warn("Could not verify if root URLs match", "plugin", plugin.JSONData.ID, "rootUrls", manifest.RootURLs)
			return plugins.Signature{}, err
		} else if !match {
			s.log.Warn("Could not find root URL that matches running application URL", "plugin", plugin.JSONData.ID,
				"appUrl", setting.AppUrl, "rootUrls", manifest.RootURLs)
			return plugins.Signature{
				Status: plugins.SignatureStatusInvalid,
			}, nil
		}
	}

	manifestFiles := make(map[string]struct{}, len(manifest.Files))

	// Verify the manifest contents
	for p, hash := range manifest.Files {
		err = verifyHash(s.log, plugin, p, hash)
		if err != nil {
			return plugins.Signature{
				Status: plugins.SignatureStatusModified,
			}, nil
		}

		manifestFiles[p] = struct{}{}
	}

	// Track files missing from the manifest
	var unsignedFiles []string
	for _, f := range fsFiles {
		// Ensure slashes are used, because MANIFEST.txt always uses slashes regardless of the filesystem
		f = toSlash(f)

		// Ignoring unsigned Chromium debug.log so it doesn't invalidate the signature for Renderer plugin running on Windows
		if runningWindows && plugin.JSONData.Type == plugins.TypeRenderer && f == "chrome-win/debug.log" {
			continue
		}

		if f == "MANIFEST.txt" {
			continue
		}
		if _, exists := manifestFiles[f]; !exists {
			unsignedFiles = append(unsignedFiles, f)
		}
	}

	if len(unsignedFiles) > 0 {
		s.log.Warn("The following files were not included in the signature", "plugin", plugin.JSONData.ID, "files", unsignedFiles)
		return plugins.Signature{
			Status: plugins.SignatureStatusModified,
		}, nil
	}

	s.log.Debug("Plugin signature valid", "id", plugin.JSONData.ID)
	return plugins.Signature{
		Status:     plugins.SignatureStatusValid,
		Type:       manifest.SignatureType,
		SigningOrg: manifest.SignedByOrgName,
	}, nil
}

func verifyHash(mlog log.Logger, plugin plugins.FoundPlugin, path, hash string) error {
	path = fromSlash(path)

	// nolint:gosec
	// We can ignore the gosec G304 warning on this one because `path` is based
	// on the path provided in a manifest file for a plugin and not user input.
	f, err := plugin.FS.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			mlog.Warn("Could not open plugin file due to lack of permissions", "plugin", plugin.JSONData.ID, "path", path)
			return errors.New("permission denied when attempting to read plugin file")
		}
		mlog.Warn("Plugin file listed in the manifest was not found", "plugin", plugin.JSONData.ID, "path", path)
		return errors.New("plugin file listed in the manifest was not found")
	}
	defer func() {
		if err := f.Close(); err != nil {
			mlog.Warn("Failed to close plugin file", "path", path, "err", err)
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return errors.New("could not calculate plugin file checksum")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != hash {
		mlog.Warn("Plugin file checksum does not match signature checksum", "plugin", plugin.JSONData.ID, "path", path)
		return errors.New("plugin file checksum does not match signature checksum")
	}

	return nil
}

func urlMatch(specs []string, target string, signatureType plugins.SignatureType) (bool, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return false, err
	}

	for _, spec := range specs {
		specURL, err := url.Parse(spec)
		if err != nil {
			return false, err
		}

		if specURL.Scheme == targetURL.Scheme && specURL.Host == targetURL.Host &&
			path.Clean(specURL.RequestURI()) == path.Clean(targetURL.RequestURI()) {
			return true, nil
		}

		if signatureType != plugins.SignatureTypePrivateGlob {
			continue
		}

		sp, err := glob.Compile(spec, '/', '.')
		if err != nil {
			return false, err
		}
		if match := sp.Match(target); match {
			return true, nil
		}
	}
	return false, nil
}

type invalidFieldErr struct {
	field string
}

func (r invalidFieldErr) Error() string {
	return fmt.Sprintf("valid manifest field %s is required", r.field)
}

func (s *Signature) validateManifest(ctx context.Context, m PluginManifest, block *clearsign.Block) error {
	if len(m.Plugin) == 0 {
		return invalidFieldErr{field: "plugin"}
	}
	if len(m.Version) == 0 {
		return invalidFieldErr{field: "version"}
	}
	if len(m.KeyID) == 0 {
		return invalidFieldErr{field: "keyId"}
	}
	if m.Time == 0 {
		return invalidFieldErr{field: "time"}
	}
	if len(m.Files) == 0 {
		return invalidFieldErr{field: "files"}
	}
	if m.isV2() {
		if len(m.SignedByOrg) == 0 {
			return invalidFieldErr{field: "signedByOrg"}
		}
		if len(m.SignedByOrgName) == 0 {
			return invalidFieldErr{field: "signedByOrgName"}
		}
		if !m.SignatureType.IsValid() {
			return fmt.Errorf("%s is not a valid signature type", m.SignatureType)
		}
	}

	return s.Verify(ctx, m.KeyID, block)
}

func (s *Signature) Verify(ctx context.Context, keyID string, block *clearsign.Block) error {
	publicKey, err := s.kr.GetPublicKey(ctx, keyID)
	if err != nil {
		return err
	}

	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewBufferString(publicKey))
	if err != nil {
		return fmt.Errorf("%v: %w", "failed to parse public key", err)
	}

	if _, err = openpgp.CheckDetachedSignature(keyring,
		bytes.NewBuffer(block.Bytes),
		block.ArmoredSignature.Body, &packet.Config{}); err != nil {
		return fmt.Errorf("failed to check signature: %w", err)
	}

	return nil
}
