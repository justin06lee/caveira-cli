package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/justin06lee/caveira/core/config"
)

// Who may have built a rea package: morluto/rea's release workflow on
// GitHub Actions, run from main or from the release's own branch
// (release/6.7.0 built 6.7.0), at the commit the release tag points at.
const (
	reaSourceRepo = "https://github.com/" + reaRepo
	reaWorkflow   = reaSourceRepo + "/.github/workflows/release.yml"
	reaIssuer     = "https://token.actions.githubusercontent.com"
	slsaV1        = "https://slsa.dev/provenance/v1"
)

// reaSigner is the certificate identity a build of version may have.
func reaSigner(version string) string {
	return "^" + regexp.QuoteMeta(reaWorkflow+"@refs/heads/") + "(main|" + regexp.QuoteMeta("release/"+version) + ")$"
}

// reaTrustedRoot is Sigstore's public keys and logs, kept current through
// its TUF repository and cached under ~/.caveira/sigstore.
func reaTrustedRoot() (root.TrustedMaterial, error) {
	opts := tuf.DefaultOptions()
	opts.CachePath = filepath.Join(config.Dir(), "sigstore")
	client, err := tuf.New(opts)
	if err != nil {
		return nil, fmt.Errorf("sigstore trust root: %w", err)
	}
	tr, err := root.GetTrustedRoot(client)
	if err != nil {
		return nil, fmt.Errorf("sigstore trust root: %w", err)
	}
	return tr, nil
}

// verifyReaProvenance checks the SLSA provenance npm keeps for rea-agents
// at version against the package's SHA-512: the bundle is signed by a
// certificate Sigstore issued to morluto/rea's release workflow (from main
// or release/<version>), that
// signing is in the public transparency log, the statement is about
// pkg:npm/rea-agents@version with this exact digest, and it was built
// from commit, the one the release tag points at. Any of those failing
// means the package is not installed.
func verifyReaProvenance(trusted root.TrustedMaterial, bundleJSON []byte, version string, sha512 []byte, commit string) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	v, err := verify.NewVerifier(trusted,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	san, err := verify.NewSANMatcher("", reaSigner(version))
	if err != nil {
		return err
	}
	issuer, err := verify.NewIssuerMatcher(reaIssuer, "")
	if err != nil {
		return err
	}
	id, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		SourceRepositoryURI:    reaSourceRepo,
		SourceRepositoryDigest: commit,
	})
	if err != nil {
		return err
	}
	res, err := v.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha512", sha512), verify.WithCertificateIdentity(id)))
	if err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	if res.Statement == nil || res.Statement.GetPredicateType() != slsaV1 {
		return errors.New("provenance: not a SLSA provenance statement")
	}
	want := "pkg:npm/" + reaPackage + "@" + version
	for _, s := range res.Statement.GetSubject() {
		if s.GetName() != want {
			return fmt.Errorf("provenance: it is about %s, not %s", s.GetName(), want)
		}
	}
	return nil
}

// reaProvenanceBundle picks the SLSA provenance out of npm's attestations
// for a package.
func reaProvenanceBundle(attestations []byte) ([]byte, error) {
	var a struct {
		Attestations []struct {
			PredicateType string          `json:"predicateType"`
			Bundle        json.RawMessage `json:"bundle"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(attestations, &a); err != nil {
		return nil, fmt.Errorf("attestations: %w", err)
	}
	for _, x := range a.Attestations {
		if x.PredicateType == slsaV1 {
			return x.Bundle, nil
		}
	}
	return nil, errors.New("the package has no build provenance")
}

// npmSHA512 is the digest in an npm integrity string ("sha512-<base64>").
func npmSHA512(integrity string) ([]byte, error) {
	for _, part := range strings.Fields(integrity) {
		if b64, ok := strings.CutPrefix(part, "sha512-"); ok {
			d, err := base64.StdEncoding.DecodeString(b64)
			if err == nil && len(d) == 64 {
				return d, nil
			}
		}
	}
	return nil, fmt.Errorf("no sha512 in the registry's integrity %q", integrity)
}
