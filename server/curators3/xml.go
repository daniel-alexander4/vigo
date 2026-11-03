// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package curators3

import (
	"encoding/xml"
	"io"
	"net/http"
)

// S3 wire constants.
const (
	// bucketName is the single fixed bucket the API exposes (ADR-027).
	bucketName = "vigo-curator"
	// s3XMLNS is the namespace S3 SDKs expect on list responses.
	s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"
)

// s3ErrorXML is the S3 error response body — `{Code, Message}` is what every
// S3 SDK parses to surface a useful error.
type s3ErrorXML struct {
	XMLName  xml.Name `xml:"Error"`
	Code     string   `xml:"Code"`
	Message  string   `xml:"Message"`
	Resource string   `xml:"Resource,omitempty"`
}

// writeS3Error renders an S3 XML error with the given HTTP status. HEAD
// requests get the status only (no body) — S3 clients read the status code.
func writeS3Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(s3ErrorXML{Code: code, Message: message, Resource: r.URL.Path})
}

// writeS3XML renders a 200 S3 XML document (list responses).
func writeS3XML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(v)
}

// --- ListObjectsV2 ---

type listBucketResultXML struct {
	XMLName               xml.Name          `xml:"ListBucketResult"`
	XMLNS                 string            `xml:"xmlns,attr"`
	Name                  string            `xml:"Name"`
	Prefix                string            `xml:"Prefix"`
	Delimiter             string            `xml:"Delimiter,omitempty"`
	MaxKeys               int               `xml:"MaxKeys"`
	KeyCount              int               `xml:"KeyCount"`
	IsTruncated           bool              `xml:"IsTruncated"`
	ContinuationToken     string            `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string            `xml:"NextContinuationToken,omitempty"`
	Contents              []s3ObjectXML     `xml:"Contents"`
	CommonPrefixes        []commonPrefixXML `xml:"CommonPrefixes"`
}

type s3ObjectXML struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         uint64 `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type commonPrefixXML struct {
	Prefix string `xml:"Prefix"`
}

// --- ListBuckets ---

type listAllMyBucketsResultXML struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	XMLNS   string   `xml:"xmlns,attr"`
	Buckets struct {
		Bucket []bucketXML `xml:"Bucket"`
	} `xml:"Buckets"`
}

type bucketXML struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

// --- GetBucketLocation ---

type locationConstraintXML struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	XMLNS   string   `xml:"xmlns,attr"`
}
