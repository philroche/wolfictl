package vex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	purl "github.com/package-url/packageurl-go"
	"gopkg.in/yaml.v3"

	"chainguard.dev/apko/pkg/sbom/generator/spdx"
	"chainguard.dev/melange/pkg/build"

	"chainguard.dev/vex/pkg/ctl"
	"chainguard.dev/vex/pkg/vex"
)

type Config struct {
	Distro, Author, AuthorRole string
}

// FromPackageConfiguration generates a new VEX document for the Wolfi package described by the build.Configuration.
func FromPackageConfiguration(vexCfg Config, buildCfg ...*build.Configuration) (*vex.VEX, error) {
	id, err := generateDocumentID(buildCfg)
	if err != nil {
		return nil, fmt.Errorf("generating doc ID: %w", err)
	}

	docs := []*vex.VEX{}
	for _, conf := range buildCfg {
		subdoc := vex.New()
		purls := conf.PackageURLs(vexCfg.Distro)
		subdoc.Statements = statementsFromConfiguration(conf, *subdoc.Timestamp, purls)
		docs = append(docs, &subdoc)
	}

	mergeOpts := &ctl.MergeOptions{
		DocumentID: id,
		Author:     vexCfg.Author,
		AuthorRole: vexCfg.AuthorRole,
	}

	vexctl := ctl.New()
	doc, err := vexctl.Merge(context.Background(), mergeOpts, docs)
	if err != nil {
		return nil, fmt.Errorf("merging vex documents: %w", err)
	}
	return doc, nil
}

// extractSBOMPurls reads an SBOM and returns the purls identifying
// packages from the distribution.
func extractSBOMPurls(vexCfg Config, sbom *spdx.Document) ([]purl.PackageURL, error) {
	purls := []purl.PackageURL{}
	for i := range sbom.Packages {
		for _, ref := range sbom.Packages[i].ExternalRefs {
			if ref.Type != "purl" {
				continue
			}

			p, err := purl.FromString(ref.Locator)
			if err != nil {
				return nil, fmt.Errorf("parsing purl: %s: %w", ref.Locator, err)
			}

			if p.Namespace == vexCfg.Distro {
				purls = append(purls, p)
			}
		}
	}
	return purls, nil
}

// parseSBOM gets an SPDX-json file and returns a parsed SBOM
func parseSBOM(sbomPath string) (*spdx.Document, error) {
	sbom := &spdx.Document{}
	data, err := os.ReadFile(sbomPath)
	if err != nil {
		return nil, fmt.Errorf("opening SBOM file: %w", err)
	}

	if err := json.Unmarshal(data, sbom); err != nil {
		return nil, fmt.Errorf("unmarshaling SBOM data: %w", err)
	}

	return sbom, nil
}

func statementsFromConfiguration(cfg *build.Configuration, documentTimestamp time.Time, purls []string) []vex.Statement {
	// We should also add a lint rule for when advisories obviate particular secfixes items.
	secfixesStatements := statementsFromSecfixes(cfg.Secfixes, purls)
	advisoriesStatements := statementsFromAdvisories(cfg.Advisories, purls)

	// don't include "not_affected" statements from secfixes that are obviated
	// by statements from advisories
	notAffectedVulns := make(map[string]struct{})
	for i := range advisoriesStatements {
		if advisoriesStatements[i].Status == vex.StatusNotAffected {
			notAffectedVulns[advisoriesStatements[i].Vulnerability] = struct{}{}
		}
	}
	var statements []vex.Statement
	for i := range secfixesStatements {
		if _, seen := notAffectedVulns[secfixesStatements[i].Vulnerability]; !seen {
			statements = append(statements, secfixesStatements[i])
		}
	}

	statements = append(statements, advisoriesStatements...)

	// TODO: also find and weed out duplicate "fixed" statements
	vex.SortStatements(statements, documentTimestamp)
	return statements
}

func statementsFromAdvisories(advisories build.Advisories, purls []string) []vex.Statement {
	var stmts []vex.Statement

	for v, entries := range advisories {
		for i := range entries {
			stmts = append(stmts, statementFromAdvisoryContent(&entries[i], v, purls))
		}
	}

	return stmts
}

func statementFromAdvisoryContent(
	content *build.AdvisoryContent, vulnerability string, purls []string,
) vex.Statement {
	return vex.Statement{
		Vulnerability:   vulnerability,
		Status:          content.Status,
		Justification:   content.Justification,
		ActionStatement: content.ActionStatement,
		ImpactStatement: content.ImpactStatement,
		Products:        purls,
		Timestamp:       &content.Timestamp,
	}
}

func statementsFromSecfixes(secfixes build.Secfixes, purls []string) []vex.Statement {
	var stmts []vex.Statement

	for packageVersion, vulnerabilities := range secfixes {
		for _, v := range vulnerabilities {
			stmts = append(stmts, statementFromSecfixesItem(packageVersion, v, purls))
		}
	}

	return stmts
}

func statementFromSecfixesItem(pkgVersion, vulnerability string, purls []string) vex.Statement {
	status := determineStatus(pkgVersion)

	return vex.Statement{
		Vulnerability: vulnerability,
		Status:        status,
		Products:      purls,
	}
}

func determineStatus(packageVersion string) vex.Status {
	if packageVersion == "0" {
		return vex.StatusNotAffected
	}

	return vex.StatusFixed
}

// generateDocumentID generate a deterministic document ID based
// on the configuration data contents
func generateDocumentID(configs []*build.Configuration) (string, error) {
	hashes := []string{}
	for _, c := range configs {
		data, err := yaml.Marshal(c)
		if err != nil {
			return "", fmt.Errorf("marshaling melange configuration: %w", err)
		}
		h := sha256.New()
		if _, err := h.Write(data); err != nil {
			return "", fmt.Errorf("hashing melange configuration: %w", err)
		}
		hashes = append(hashes, fmt.Sprintf("%x", h.Sum(nil)))
	}

	sort.Strings(hashes)
	h := sha256.New()
	if _, err := h.Write([]byte(strings.Join(hashes, ":"))); err != nil {
		return "", fmt.Errorf("hashing config files: %w", err)
	}

	// One hash to rule them all
	return fmt.Sprintf("vex-%s", fmt.Sprintf("%x", h.Sum(nil))), nil
}
