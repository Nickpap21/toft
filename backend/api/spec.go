// Package api carries the OpenAPI document, the API's source of truth
// (D-18, D-59). The backend serves it at GET /api/v1/openapi.yaml; the
// route table test and the response checks read it; the web client's types
// and the toft CLI are generated from it.
package api

import _ "embed"

//go:embed openapi.yaml
var Spec []byte

// Docs is the reference page that renders Spec (GET /api/v1/docs, D-60).
//
//go:embed docs.html
var Docs []byte
