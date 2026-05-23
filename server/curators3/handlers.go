// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package curators3

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
)

// maxKeysDefault / maxKeysCap bound a ListObjectsV2 page (S3's defaults).
const (
	maxKeysDefault = 1000
	maxKeysCap     = 1000
)

// serveObject handles GetObject and HeadObject. The object key is
// `<publisher>/<artifact>/<version-or-tag>/<os>-<arch>`; it resolves to a
// curator blob, which is streamed via http.ServeContent (HTTP Range native;
// it also writes headers-only for HEAD).
func (s *Server) serveObject(w http.ResponseWriter, r *http.Request, resolved *Resolved, key string) {
	publisher, artifact, versionOrTag, goos, goarch, ok := parseObjectKey(key)
	if !ok {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "the key is not a valid curator artifact path")
		return
	}
	cat, blocked, err := s.catalog.FleetSnapshot(r.Context())
	if err != nil {
		writeS3Error(w, r, http.StatusInternalServerError, "InternalError", "building the catalog")
		return
	}
	_, byName := s.nameMaps(r.Context())
	pubkeyHex := pubkeyForPublisher(publisher, byName)
	if pubkeyHex == "" {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "unknown publisher")
		return
	}
	if !scopeAllows(resolved, pubkeyHex, byName) {
		writeS3Error(w, r, http.StatusForbidden, "AccessDenied", "the credential is not scoped to this publisher")
		return
	}
	entry := cat.LookupByName(pubkeyHex, artifact)
	if entry == nil {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "no such artifact")
		return
	}
	if aid, aerr := curatormesh.ArtifactID(*entry); aerr == nil && isBlocked(blocked, aid) {
		// An operator-blocked artifact is invisible over S3.
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "no such artifact")
		return
	}
	version, vok := curatormesh.ResolveVersion(entry, versionOrTag)
	if !vok {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "no such version or tag")
		return
	}
	platform := curatormesh.ResolvePlatform(entry, version, goos, goarch)
	if platform == nil {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "no such platform build")
		return
	}
	f, err := os.Open(filepath.Join(s.blobsDir, platform.BlobSha))
	if err != nil {
		writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", "the artifact blob is not held by this server")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeS3Error(w, r, http.StatusInternalServerError, "InternalError", "stat blob")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+platform.BlobSha+`"`) // curator is content-addressed
	if entry.Body.Kind != "" {
		w.Header().Set("x-amz-meta-vigo-kind", entry.Body.Kind)
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// listObjectsV2 projects the curator catalog into S3 object keys. Each
// `{version|tag} × platform` of every non-blocked artifact in scope is a key;
// `prefix` / `delimiter=/` / `max-keys` / `continuation-token` follow S3
// ListObjectsV2 semantics.
func (s *Server) listObjectsV2(w http.ResponseWriter, r *http.Request, resolved *Resolved) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	maxKeys := parseMaxKeys(q.Get("max-keys"))
	contRaw := q.Get("continuation-token")
	contToken := decodeToken(contRaw)

	cat, blocked, err := s.catalog.FleetSnapshot(r.Context())
	if err != nil {
		writeS3Error(w, r, http.StatusInternalServerError, "InternalError", "building the catalog")
		return
	}
	byPubkey, byName := s.nameMaps(r.Context())

	var objs []s3ObjectXML
	for i := range cat.Artifacts {
		e := &cat.Artifacts[i]
		aid, aerr := curatormesh.ArtifactID(*e)
		if aerr != nil || isBlocked(blocked, aid) {
			continue
		}
		if !scopeAllows(resolved, strings.ToLower(e.Body.FounderPuddlePubkey), byName) {
			continue
		}
		publisher := publisherForPubkey(e.Body.FounderPuddlePubkey, byPubkey)
		lastMod := s3Time(e.Body.IssuedAt)
		emit := func(segment string, p *curatormesh.Platform) {
			objs = append(objs, s3ObjectXML{
				Key:          publisher + "/" + e.Body.Name + "/" + segment + "/" + p.OS + "-" + p.Arch,
				LastModified: lastMod,
				ETag:         `"` + p.BlobSha + `"`,
				Size:         p.Size,
				StorageClass: "STANDARD",
			})
		}
		for ver, av := range e.Body.Versions {
			for j := range av.Platforms {
				emit(ver, &av.Platforms[j])
			}
		}
		// Tags are alias keys — `…/latest/os-arch` resolves like the version.
		for tag, ver := range e.Body.Tags {
			av, ok := e.Body.Versions[ver]
			if !ok {
				continue // dangling tag
			}
			for j := range av.Platforms {
				emit(tag, &av.Platforms[j])
			}
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })

	result := listBucketResultXML{
		XMLNS: s3XMLNS, Name: bucketName, Prefix: prefix, Delimiter: delimiter, MaxKeys: maxKeys,
	}
	if contRaw != "" {
		result.ContinuationToken = contRaw
	}
	seenPrefix := map[string]bool{}
	count := 0
	for _, obj := range objs {
		if !strings.HasPrefix(obj.Key, prefix) {
			continue
		}
		if contToken != "" && obj.Key <= contToken {
			continue
		}
		if delimiter != "" {
			rest := obj.Key[len(prefix):]
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				cp := prefix + rest[:idx+len(delimiter)]
				if seenPrefix[cp] {
					continue
				}
				if count >= maxKeys {
					result.IsTruncated = true
					result.NextContinuationToken = encodeToken(obj.Key)
					break
				}
				seenPrefix[cp] = true
				result.CommonPrefixes = append(result.CommonPrefixes, commonPrefixXML{Prefix: cp})
				count++
				continue
			}
		}
		if count >= maxKeys {
			result.IsTruncated = true
			result.NextContinuationToken = encodeToken(obj.Key)
			break
		}
		result.Contents = append(result.Contents, obj)
		count++
	}
	result.KeyCount = count
	writeS3XML(w, result)
}

// listBuckets returns the one fixed bucket.
func (s *Server) listBuckets(w http.ResponseWriter, _ *http.Request) {
	var doc listAllMyBucketsResultXML
	doc.XMLNS = s3XMLNS
	doc.Buckets.Bucket = []bucketXML{{
		Name:         bucketName,
		CreationDate: time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05.000Z"),
	}}
	writeS3XML(w, doc)
}

// getBucketLocation returns an empty LocationConstraint (the S3 us-east-1
// convention) — the API has no region.
func (s *Server) getBucketLocation(w http.ResponseWriter, _ *http.Request) {
	writeS3XML(w, locationConstraintXML{XMLNS: s3XMLNS})
}

// --- key + helper functions ---

// parseObjectKey splits `<publisher>/<artifact>/<version-or-tag>/<os>-<arch>`.
func parseObjectKey(key string) (publisher, artifact, version, goos, goarch string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", "", "", false
	}
	o, a, found := strings.Cut(parts[3], "-")
	if !found || o == "" || a == "" {
		return "", "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], o, a, true
}

// pubkeyForPublisher maps a `<publisher>` key segment to a founder-pubkey hex:
// a 64-hex segment is the pubkey itself; otherwise it's a friendly name
// resolved via the fleet name map. Empty string when a name doesn't resolve.
func pubkeyForPublisher(publisher string, byName map[string]string) string {
	if isHex64(publisher) {
		return strings.ToLower(publisher)
	}
	return byName[publisher]
}

// publisherForPubkey renders the `<publisher>` segment for a founder pubkey:
// the unambiguous friendly name when one exists, else the hex.
func publisherForPubkey(pubkeyHex string, byPubkey map[string]string) string {
	if nm, ok := byPubkey[strings.ToLower(pubkeyHex)]; ok {
		return nm
	}
	return strings.ToLower(pubkeyHex)
}

// scopeAllows reports whether a credential scoped to `resolved.Row.Scope` may
// read an object under the given founder pubkey. `*` = every publisher;
// otherwise the scope (a publisher name or hex) must resolve to the same
// founder pubkey as the object's.
func scopeAllows(resolved *Resolved, objectPubkeyHex string, byName map[string]string) bool {
	scope := resolved.Row.Scope
	if scope == "*" || scope == "" {
		return true
	}
	scopePubkey := pubkeyForPublisher(scope, byName)
	return scopePubkey != "" && strings.EqualFold(scopePubkey, objectPubkeyHex)
}

func isBlocked(blocked []db.CuratorBlockedArtifact, artifactID string) bool {
	for _, b := range blocked {
		if strings.EqualFold(b.ArtifactID, artifactID) {
			return true
		}
	}
	return false
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func parseMaxKeys(s string) int {
	if s == "" {
		return maxKeysDefault
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxKeysCap {
		return maxKeysCap
	}
	return n
}

// s3Time renders a curator `issued_at` (unix millis) as an S3 ISO-8601 stamp.
func s3Time(issuedAtMillis int64) string {
	if issuedAtMillis <= 0 {
		return time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return time.UnixMilli(issuedAtMillis).UTC().Format("2006-01-02T15:04:05.000Z")
}

// continuation tokens are an opaque base64 of the last key emitted.
func encodeToken(key string) string { return base64.StdEncoding.EncodeToString([]byte(key)) }

func decodeToken(tok string) string {
	if tok == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(tok)
	if err != nil {
		return ""
	}
	return string(b)
}
