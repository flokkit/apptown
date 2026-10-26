package instanceupdate

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	ct "github.com/google/certificate-transparency-go"
	cttls "github.com/google/certificate-transparency-go/tls"
	ctx509 "github.com/google/certificate-transparency-go/x509"
	ctx509util "github.com/google/certificate-transparency-go/x509util"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	rekortilespb "github.com/sigstore/rekor-tiles/v2/pkg/generated/protobuf"
	rekornote "github.com/sigstore/rekor-tiles/v2/pkg/note"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	testca "github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/signature"
	logformat "github.com/transparency-dev/formats/log"
	"github.com/transparency-dev/merkle/rfc6962"
	"golang.org/x/mod/sumdb/note"
	"google.golang.org/protobuf/encoding/protojson"
)

type signedFixture struct {
	tag                                   string
	manifest, checksums, notes, signature []byte
	trust                                 root.TrustedMaterial
}

type fixtureTrust struct {
	*testca.VirtualSigstore
	ca      root.CertificateAuthority
	logTime time.Time
	logs    map[string]*root.TransparencyLog
	ct      map[string]*root.TransparencyLog
}

func (f fixtureTrust) FulcioCertificateAuthorities() []root.CertificateAuthority {
	return []root.CertificateAuthority{f.ca}
}
func (f fixtureTrust) RekorLogs() map[string]*root.TransparencyLog {
	if f.logs != nil {
		return f.logs
	}
	logs := f.VirtualSigstore.RekorLogs()
	for _, log := range logs {
		log.ValidityPeriodStart = f.logTime.Add(-time.Hour)
		log.ValidityPeriodEnd = f.logTime.Add(time.Hour)
	}
	return logs
}
func (f fixtureTrust) CTLogs() map[string]*root.TransparencyLog { return f.ct }

func fixtureKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func fixtureCert(t *testing.T, template, parent *x509.Certificate, public any, key crypto.Signer) *x509.Certificate {
	t.Helper()
	raw, err := x509.CreateCertificate(rand.Reader, template, parent, public, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func fixtureExtension(t *testing.T, oid asn1.ObjectIdentifier, value string) pkix.Extension {
	t.Helper()
	raw, err := asn1.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return pkix.Extension{Id: oid, Value: raw}
}

// This controlled PKI exercises the real chain, SCT, Rekor inclusion, artifact
// and identity verifier. No production Unsafe options or verification fake.
func signatureFixture(t *testing.T, tag, commit, signer, oidc string, sums []byte, message ...bool) ([]byte, root.TrustedMaterial) {
	t.Helper()
	return signatureFixtureAt(t, tag, commit, signer, oidc, sums, time.Now().UTC(), message...)
}
func signatureFixtureAt(t *testing.T, tag, commit, signer, oidc string, sums []byte, now time.Time, message ...bool) ([]byte, root.TrustedMaterial) {
	t.Helper()
	virtual, err := testca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	caKey, leafKey, ctKey := fixtureKey(t), fixtureKey(t), fixtureKey(t)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test release root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caCert := fixtureCert(t, caTemplate, caTemplate, caKey.Public(), caKey)
	uri, err := url.Parse(signer)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), URIs: []*url.URL{uri}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}, ExtraExtensions: []pkix.Extension{
		fixtureExtension(t, certificate.OIDIssuerV2, oidc), fixtureExtension(t, certificate.OIDSourceRepositoryURI, publisher), fixtureExtension(t, certificate.OIDSourceRepositoryDigest, commit), fixtureExtension(t, certificate.OIDSourceRepositoryRef, "refs/tags/"+tag),
	}}
	precert := fixtureCert(t, leafTemplate, caCert, leafKey.Public(), caKey)
	ctDER, err := x509.MarshalPKIXPublicKey(ctKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	ctID := sha256.Sum256(ctDER)
	sct := ct.SignedCertificateTimestamp{SCTVersion: ct.V1, LogID: ct.LogID{KeyID: ctID}, Timestamp: uint64(now.UnixMilli())}
	logEntry := ct.LogEntry{Leaf: ct.MerkleTreeLeaf{TimestampedEntry: &ct.TimestampedEntry{Timestamp: sct.Timestamp, EntryType: ct.PrecertLogEntryType, PrecertEntry: &ct.PreCert{IssuerKeyHash: sha256.Sum256(caCert.RawSubjectPublicKeyInfo), TBSCertificate: precert.RawTBSCertificate}}}}
	serialized, err := ct.SerializeSCTSignatureInput(sct, logEntry)
	if err != nil {
		t.Fatal(err)
	}
	hashed := sha256.Sum256(serialized)
	signed, err := ctKey.Sign(rand.Reader, hashed[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	sct.Signature = ct.DigitallySigned{Algorithm: cttls.SignatureAndHashAlgorithm{Hash: cttls.SHA256, Signature: cttls.ECDSA}, Signature: signed}
	scts, err := ctx509util.MarshalSCTsIntoSCTList([]*ct.SignedCertificateTimestamp{&sct})
	if err != nil {
		t.Fatal(err)
	}
	list, err := cttls.Marshal(*scts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := asn1.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate.ExtraExtensions = append(leafTemplate.ExtraExtensions, pkix.Extension{Id: asn1.ObjectIdentifier(ctx509.OIDExtensionCTSCT), Value: encoded})
	leaf := fixtureCert(t, leafTemplate, caCert, leafKey.Public(), caKey)
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "subject": []any{map[string]any{"name": ChecksumsName, "digest": map[string]string{"sha256": hashBytes(sums)}}}, "predicateType": "https://apptown.dev/release-checksums/v1", "predicate": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	payloadType := "application/vnd.in-toto+json"
	pae := sha256.Sum256(dsse.PAE(payloadType, statement))
	sig, err := leafKey.Sign(rand.Reader, pae[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	envelope := &dsse.Envelope{PayloadType: payloadType, Payload: base64.StdEncoding.EncodeToString(statement), Signatures: []dsse.Signature{{Sig: base64.StdEncoding.EncodeToString(sig)}}}

	var entry *tlog.Entry
	protobuf := &protobundle.Bundle{MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json"}
	kind := "dsse"
	if len(message) > 0 && message[0] {
		kind = "hashedrekord"
		digest := sha256.Sum256(sums)
		sig, err = leafKey.Sign(rand.Reader, digest[:], crypto.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		body, err := json.Marshal(map[string]any{"apiVersion": "0.0.1", "kind": "hashedrekord", "spec": map[string]any{"data": map[string]any{"hash": map[string]string{"algorithm": "sha256", "value": hex.EncodeToString(digest[:])}}, "signature": map[string]any{"content": base64.StdEncoding.EncodeToString(sig), "publicKey": map[string]string{"content": base64.StdEncoding.EncodeToString(certificatePEM)}}}})
		if err != nil {
			t.Fatal(err)
		}
		logID, err := virtual.RekorLogID()
		if err != nil {
			t.Fatal(err)
		}
		logKey, err := hex.DecodeString(logID)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := virtual.GetInclusionProof(body)
		if err != nil {
			t.Fatal(err)
		}
		rootHash, err := hex.DecodeString(*proof.RootHash)
		if err != nil {
			t.Fatal(err)
		}
		hashes := make([][]byte, len(proof.Hashes))
		for i, hash := range proof.Hashes {
			hashes[i], err = hex.DecodeString(hash)
			if err != nil {
				t.Fatal(err)
			}
		}
		entry, err = tlog.NewTlogEntry(&protorekor.TransparencyLogEntry{LogIndex: 0, LogId: &protocommon.LogId{KeyId: logKey}, IntegratedTime: now.Unix(), CanonicalizedBody: body, KindVersion: &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.1"}, InclusionProof: &protorekor.InclusionProof{LogIndex: *proof.LogIndex, TreeSize: *proof.TreeSize, RootHash: rootHash, Hashes: hashes, Checkpoint: &protorekor.Checkpoint{Envelope: *proof.Checkpoint}}})
		if err != nil {
			t.Fatal(err)
		}
		protobuf.Content = &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: digest[:]}, Signature: sig}}
	} else {
		entry, err = virtual.GenerateTlogEntry(leaf, envelope, sig, now.Unix(), true)
		if err != nil {
			t.Fatal(err)
		}
		protobuf.Content = &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{Payload: statement, PayloadType: payloadType, Signatures: []*protodsse.Signature{{Sig: sig}}}}
	}
	// Legacy fixture constructors keep kind/version and SET outside the proto.
	// Serialize both using the controlled log key, without unsafe verifier options.
	tle := entry.TransparencyLogEntry()
	tle.KindVersion = &protorekor.KindVersion{Kind: kind, Version: "0.0.1"}
	set, err := virtual.RekorSignPayload(tlog.RekorPayload{Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody), IntegratedTime: tle.IntegratedTime, LogIndex: tle.LogIndex, LogID: hex.EncodeToString(tle.LogId.KeyId)})
	if err != nil {
		t.Fatal(err)
	}
	tle.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	protobuf.VerificationMaterial = &protobundle.VerificationMaterial{Content: &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw}}, TlogEntries: []*protorekor.TransparencyLogEntry{tle}}
	data, err := protojson.Marshal(protobuf)
	if err != nil {
		t.Fatal(err)
	}
	return data, fixtureTrust{VirtualSigstore: virtual, logTime: now, ca: &root.FulcioCertificateAuthority{Root: caCert, ValidityPeriodStart: now.Add(-time.Hour), ValidityPeriodEnd: now.Add(time.Hour)}, ct: map[string]*root.TransparencyLog{hex.EncodeToString(ctID[:]): {ID: ctID[:], HashFunc: crypto.SHA256, PublicKey: ctKey.Public(), ValidityPeriodStart: now.Add(-time.Hour), ValidityPeriodEnd: now.Add(time.Hour)}}}
}

func newSignedFixture(t *testing.T) signedFixture {
	t.Helper()
	f := signedFixture{tag: "v1.1.0", notes: []byte("## Release\nSigned notes.\n")}
	commit := strings.Repeat("a", 40)
	m := Manifest{Format: 1, Version: f.tag, Commit: commit, Notes: Artifact{Name: NotesName, SHA256: hashBytes(f.notes), Size: int64(len(f.notes))}, Archives: map[string]Archive{}, Images: map[string]Image{}, Compatibility: Compatibility{Schema: Range{167, 177}, Layout: Range{1, 2}, SchemaResult: 177, LayoutResult: 2, CLIProtocol: 1, DaemonWorkerProtocol: 2, WorkerProtocol: Range{1, 2}, DatabaseTransition: "same-major", RuntimeTransition: "unchanged"}}
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		members := map[string]string{}
		for _, name := range []string{"bin/apptownd", "bin/apptown", "bin/apptown-worker", "bin/apptown-linux", "deploy/apptown-agent/Dockerfile", "scripts/apptown-db-backup.sh", "share/apptown/updates/stock.toml", "deploy/systemd/apptown.service"} {
			members[name] = strings.Repeat("b", 64)
		}
		m.Archives[p] = Archive{Artifact: Artifact{Name: "apptown-1.1.0-" + strings.ReplaceAll(p, "/", "-") + ".tar.gz", SHA256: hashBytes([]byte(p)), Size: 1}, Members: members}
	}
	for _, name := range []string{"apptown", "apptown-worker", "apptown-agent"} {
		m.Images[name] = Image{Repository: "ghcr.io/flokkit/" + name, Index: "sha256:" + strings.Repeat("c", 64), Platforms: map[string]string{"linux/amd64": "sha256:" + strings.Repeat("d", 64), "linux/arm64": "sha256:" + strings.Repeat("e", 64)}}
	}
	var err error
	f.manifest, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.checksums = []byte(hashBytes(f.manifest) + "  " + ManifestName + "\n" + hashBytes(f.notes) + "  " + NotesName + "\n")
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		a := m.Archives[p]
		f.checksums = append(f.checksums, []byte(a.SHA256+"  "+a.Name+"\n")...)
	}
	f.signature, f.trust = signatureFixture(t, f.tag, commit, publisher+"/.github/workflows/images.yml@refs/tags/"+f.tag, issuer, f.checksums)
	return f
}

func (f signedFixture) verify() (signedRelease, error) {
	return (releaseVerifier{trust: func(context.Context) (root.TrustedMaterial, error) { return f.trust, nil }}).verify(context.Background(), f.tag, f.manifest, f.checksums, f.signature, f.notes)
}

func TestReleaseVerificationUsesRealIdentityChainAndArtifactProof(t *testing.T) {
	f := newSignedFixture(t)
	verified, err := f.verify()
	if err != nil {
		t.Fatal(err)
	}
	if verified.Evidence.SourceCommit != strings.Repeat("a", 40) || verified.Evidence.ManifestSHA256 != hashBytes(f.manifest) {
		t.Fatal("missing authenticated evidence")
	}
	for _, tc := range []struct{ name, tag, commit, signer, issuer string }{
		{"foreign workflow", f.tag, strings.Repeat("a", 40), publisher + "/.github/workflows/other.yml@refs/tags/" + f.tag, issuer},
		{"foreign issuer", f.tag, strings.Repeat("a", 40), publisher + "/.github/workflows/images.yml@refs/tags/" + f.tag, "https://foreign.invalid"},
		{"foreign commit", f.tag, strings.Repeat("f", 40), publisher + "/.github/workflows/images.yml@refs/tags/" + f.tag, issuer},
		{"foreign tag", "v1.2.0", strings.Repeat("a", 40), publisher + "/.github/workflows/images.yml@refs/tags/v1.2.0", issuer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := f
			bad.signature, bad.trust = signatureFixture(t, tc.tag, tc.commit, tc.signer, tc.issuer, f.checksums)
			if _, err := bad.verify(); err == nil {
				t.Fatal("accepted another signing identity")
			}
		})
	}
	t.Run("changed manifest", func(t *testing.T) {
		bad := f
		bad.manifest = []byte(strings.Replace(string(f.manifest), `"schemaResult":177`, `"schemaResult":178`, 1))
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted changed manifest bytes")
		}
	})
	t.Run("changed notes", func(t *testing.T) {
		bad := f
		bad.notes = []byte("injected notes")
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted changed notes")
		}
	})
	t.Run("missing transparency", func(t *testing.T) {
		bad := f
		var b bundle.Bundle
		if err := json.Unmarshal(f.signature, &b); err != nil {
			t.Fatal(err)
		}
		b.VerificationMaterial.TlogEntries = nil
		var err error
		bad.signature, err = json.Marshal(&b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted missing log evidence")
		}
	})
	t.Run("corrupt signature", func(t *testing.T) {
		bad := f
		var b bundle.Bundle
		if err := json.Unmarshal(f.signature, &b); err != nil {
			t.Fatal(err)
		}
		b.GetDsseEnvelope().Signatures[0].Sig[0] ^= 1
		var err error
		bad.signature, err = json.Marshal(&b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted corrupt signature")
		}
	})
}

func TestPublishedMessageSignatureAuthenticatesChecksumsAndTrustAtSigningTime(t *testing.T) {
	f := newSignedFixture(t)
	signer := publisher + "/.github/workflows/images.yml@refs/tags/" + f.tag
	f.signature, f.trust = signatureFixture(t, f.tag, strings.Repeat("a", 40), signer, issuer, f.checksums, true)
	if _, err := f.verify(); err != nil {
		t.Fatalf("published sign-blob format: %v", err)
	}

	t.Run("historical leaf expired now but valid when signed", func(t *testing.T) {
		historical := f
		historical.signature, historical.trust = signatureFixtureAt(t, f.tag, strings.Repeat("a", 40), signer, issuer, f.checksums, time.Now().Add(-2*time.Hour), true)
		var b protobundle.Bundle
		if err := protojson.Unmarshal(historical.signature, &b); err != nil {
			t.Fatal(err)
		}
		certificate, err := x509.ParseCertificate(b.VerificationMaterial.GetCertificate().RawBytes)
		if err != nil || !certificate.NotAfter.Before(time.Now()) {
			t.Fatalf("fixture leaf is not expired now: %v", err)
		}
		if _, err := historical.verify(); err != nil {
			t.Fatalf("valid historical release refused: %v", err)
		}
	})
	t.Run("signed different artifact", func(t *testing.T) {
		bad := f
		bad.signature, bad.trust = signatureFixture(t, f.tag, strings.Repeat("a", 40), signer, issuer, []byte("other artifact"), true)
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted signature over another checksum artifact")
		}
	})
	t.Run("untrusted certificate root", func(t *testing.T) {
		bad := f
		bad.trust = newSignedFixture(t).trust
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted certificate under an unrelated trust root")
		}
	})
	t.Run("root invalid at authenticated signing time", func(t *testing.T) {
		bad := f
		trust := f.trust.(fixtureTrust)
		ca := *trust.ca.(*root.FulcioCertificateAuthority)
		ca.ValidityPeriodEnd = time.Now().Add(-2 * time.Hour)
		trust.ca = &ca
		bad.trust = trust
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted trust outside its validity at signing time")
		}
	})
	t.Run("invalid inclusion proof", func(t *testing.T) {
		bad := f
		var b protobundle.Bundle
		if err := protojson.Unmarshal(f.signature, &b); err != nil {
			t.Fatal(err)
		}
		b.VerificationMaterial.TlogEntries[0].InclusionProof.RootHash[0] ^= 0xff
		var err error
		bad.signature, err = protojson.Marshal(&b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.verify(); err == nil {
			t.Fatal("accepted invalid transparency inclusion proof")
		}
	})
}

// Modern signing configurations can select Rekor v2 with a signed RFC3161
// observer timestamp. Exercise that maintained verifier path as well as v1 SETs.
func TestMessageSignatureWithRekorV2AndAuthenticatedTimestamp(t *testing.T) {
	f := newSignedFixture(t)
	f.signature, f.trust = signatureFixture(t, f.tag, strings.Repeat("a", 40), publisher+"/.github/workflows/images.yml@refs/tags/"+f.tag, issuer, f.checksums, true)
	var b protobundle.Bundle
	if err := protojson.Unmarshal(f.signature, &b); err != nil {
		t.Fatal(err)
	}
	message := b.GetMessageSignature()
	raw, err := protojson.Marshal(&rekortilespb.Entry{ApiVersion: "0.0.2", Kind: "hashedrekord", Spec: &rekortilespb.Spec{Spec: &rekortilespb.Spec_HashedRekordV002{HashedRekordV002: &rekortilespb.HashedRekordLogEntryV002{
		Data:      message.MessageDigest,
		Signature: &rekortilespb.Signature{Content: message.Signature, Verifier: &rekortilespb.Verifier{KeyDetails: protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256, Verifier: &rekortilespb.Verifier_X509Certificate{X509Certificate: b.VerificationMaterial.GetCertificate()}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	logKey := fixtureKey(t)
	der, err := x509.MarshalPKIXPublicKey(logKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	logID := sha256.Sum256(der)
	signer, err := signature.LoadECDSASignerVerifier(logKey, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	noteSigner, err := rekornote.NewNoteSigner(t.Context(), "rekor.localhost", signer)
	if err != nil {
		t.Fatal(err)
	}
	leafHash := rfc6962.DefaultHasher.HashLeaf(body)
	checkpoint := logformat.Checkpoint{Origin: "rekor.localhost", Size: 1, Hash: leafHash}
	signedCheckpoint, err := note.Sign(&note.Note{Text: string(checkpoint.Marshal())}, noteSigner)
	if err != nil {
		t.Fatal(err)
	}
	b.VerificationMaterial.TlogEntries = []*protorekor.TransparencyLogEntry{{LogIndex: 0, LogId: &protocommon.LogId{KeyId: logID[:]}, KindVersion: &protorekor.KindVersion{Kind: "hashedrekord", Version: "0.0.2"}, CanonicalizedBody: body, InclusionProof: &protorekor.InclusionProof{LogIndex: 0, TreeSize: 1, RootHash: leafHash, Hashes: [][]byte{}, Checkpoint: &protorekor.Checkpoint{Envelope: string(signedCheckpoint)}}}}
	trust := f.trust.(fixtureTrust)
	timestamp, err := trust.TimestampResponse(message.Signature)
	if err != nil {
		t.Fatal(err)
	}
	b.VerificationMaterial.TimestampVerificationData = &protobundle.TimestampVerificationData{Rfc3161Timestamps: []*protocommon.RFC3161SignedTimestamp{{SignedTimestamp: timestamp}}}
	trust.logs = map[string]*root.TransparencyLog{hex.EncodeToString(logID[:]): {BaseURL: "https://rekor.localhost", ID: logID[:], HashFunc: crypto.SHA256, SignatureHashFunc: crypto.SHA256, PublicKey: logKey.Public(), ValidityPeriodStart: time.Now().Add(-time.Hour), ValidityPeriodEnd: time.Now().Add(time.Hour)}}
	f.trust = trust
	f.signature, err = protojson.Marshal(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(); err != nil {
		t.Fatalf("Rekor v2 with signed timestamp: %v", err)
	}
	b.VerificationMaterial.TimestampVerificationData = nil
	f.signature, err = protojson.Marshal(&b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(); err == nil {
		t.Fatal("v2 entry without authenticated observer timestamp accepted")
	}
}
