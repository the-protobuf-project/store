// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package ddnrefs

// config.go is the plugin's configuration: what protoc-gen-store's repository
// target emits as ddn/references.json beside the GraphQL repositories, so
// nothing about a project is written by hand. It names, per DDN mutation root
// field, the reference fields a write sets (to resolve) or the resource a
// delete removes (to ask referrers about), and where every external resource
// type's AIP Get and every referring service's store.v1.ReferenceService is
// served.

import (
	"encoding/json"
	"fmt"
	"os"
)

// Version is the config format version this runtime reads.
const Version = 1

// Config is ddn/references.json.
type Config struct {
	Version   int        `json:"version"`
	Mutations []Mutation `json:"mutations"`
	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

// Mutation is one DDN mutation root field the plugin checks.
type Mutation struct {
	// Field is the root field ("insertLendingLoans", "updateLendingLoansById",
	// "deleteCatalogBooksById").
	Field string `json:"field"`
	// Kind is "insert", "update" or "delete".
	Kind string `json:"kind"`
	// ResourceType is the AIP type of the rows the mutation writes.
	ResourceType string `json:"resourceType"`
	// References are the reference fields an insert or update may set.
	References []Reference `json:"references,omitempty"`
	// Pattern is the resource pattern ("shelves/{shelf}/books/{book}"); a
	// delete of a top-level resource builds the name from it and the key.
	Pattern string `json:"pattern,omitempty"`
	// KeyArgument is the delete's key argument ("keyId").
	KeyArgument string `json:"keyArgument,omitempty"`
	// ByIDQuery is the query root field reading one row by key
	// ("catalogBooksById"); a delete of a nested resource reads the row's
	// name through it.
	ByIDQuery string `json:"byIdQuery,omitempty"`
}

// Reference is one reference field of a mutation's rows.
type Reference struct {
	// Field is the proto field, Column the GraphQL field DDN exposes.
	Field  string `json:"field"`
	Column string `json:"column"`
	// Type is the AIP type the field names.
	Type string `json:"type"`
	// OnDelete is its (store.v1.reference).on_delete, as the enum value name.
	OnDelete string `json:"onDelete"`
	// Repeated marks a list of names.
	Repeated bool `json:"repeated,omitempty"`
}

// Endpoint is one store.yaml reference_services: entry.
type Endpoint struct {
	Type      string   `json:"type,omitempty"`
	Get       string   `json:"get,omitempty"`
	Target    string   `json:"target,omitempty"`
	Referrers []string `json:"referrers,omitempty"`
}

// Load reads a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Version != Version {
		return nil, fmt.Errorf("%s: config version %d, this plugin reads %d", path, c.Version, Version)
	}
	return &c, nil
}
