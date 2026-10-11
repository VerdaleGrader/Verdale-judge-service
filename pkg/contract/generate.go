// Package contract holds the wire types shared with Core service, generated
// from the pinned contracts/judge.openapi.yaml. Never edit types.gen.go by
// hand: change the spec in Verdale-core-service, re-pin with
// `make contracts-sync`, then run `make generate`.
package contract

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../contracts/judge.openapi.yaml
